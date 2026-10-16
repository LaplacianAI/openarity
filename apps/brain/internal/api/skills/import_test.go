package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// fakeImporter answers every fetch with one import or one error, and records
// what it was asked for and how long it was given.
type fakeImporter struct {
	result hub.Import
	err    error
	delay  time.Duration

	mu      sync.Mutex
	sources []string
	budget  time.Duration
}

func (f *fakeImporter) Fetch(ctx context.Context, source string) (hub.Import, error) {
	f.mu.Lock()
	f.sources = append(f.sources, source)
	if deadline, ok := ctx.Deadline(); ok {
		f.budget = time.Until(deadline)
	}
	f.mu.Unlock()

	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return hub.Import{}, fmt.Errorf("%w: %w", hub.ErrUnavailable, ctx.Err())
	}
	return f.result, f.err
}

func (f *fakeImporter) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sources)
}

const (
	pdfRef = "github:acme/skills/skills/pdf@main"
	oldSHA = "1111111111111111111111111111111111111111"
	newSHA = "2222222222222222222222222222222222222222"
)

func pdfEntries(name string) []skill.Entry {
	return []skill.Entry{
		{Path: "SKILL.md", Data: []byte("---\nname: " + name + "\ndescription: Fill PDF forms.\n---\n# PDF v2\n")},
		{Path: "scripts/fill.py", Data: []byte("print('fill v2')\n")},
	}
}

func importing(sha string, entries ...skill.Entry) *fakeImporter {
	return &fakeImporter{result: hub.Import{Kind: "github", Ref: pdfRef, SHA: sha, Entries: entries}}
}

func failing(err error) *fakeImporter { return &fakeImporter{err: err} }

func importBody(t *testing.T, source string) sent {
	t.Helper()

	b, err := json.Marshal(importRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return sent{"application/json", strings.NewReader(string(b))}
}

func importPath(teamID uuid.UUID) string { return collection(teamID) + "/import" }

func syncPath(teamID, id uuid.UUID) string { return one(teamID, id) + "/sync" }

var githubOrigin = Origin{Source: "github", Ref: text(pdfRef), SHA: text(oldSHA)}

// --- import ---

// What was fetched is stored, under the origin the importer reported — the
// ref as it will be fetched again and the SHA it named — and the source is
// handed over exactly as the caller typed it.
func TestImportStoresTheSourceWithItsOrigin(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	b := newBucket(s.txStore)
	imp := importing(newSHA, pdfEntries("pdf")...)

	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID),
		importBody(t, "github:acme/skills/skills/pdf@main"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}

	got := decodeDetail(t, rec)
	if got.Name != "pdf" || got.Body != "# PDF v2\n" || got.TeamID != teamID || got.Source != "github" ||
		got.SourceRef == nil || *got.SourceRef != pdfRef || got.SourceSHA == nil || *got.SourceSHA != newSHA {
		t.Errorf("imported = %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "scripts/fill.py" {
		t.Errorf("files = %+v", got.Files)
	}
	if want := []string{"github:acme/skills/skills/pdf@main"}; !slices.Equal(imp.fetched(), want) {
		t.Errorf("fetched %q, want %q", imp.fetched(), want)
	}

	row := s.skills[got.ID]
	if row.Source != "github" || row.SourceRef == nil || *row.SourceRef != pdfRef || row.SourceSha == nil || *row.SourceSha != newSHA {
		t.Errorf("stored origin = %s %v %v", row.Source, row.SourceRef, row.SourceSha)
	}
	if rows := s.files[got.ID]; len(rows) != 1 || string(b.objects[rows[0].ObjectKey]) != "print('fill v2')\n" {
		t.Errorf("the bucket does not hold the file under its row's key: %+v", rows)
	}
}

// Each failure is answered by whose it is: the caller's to fix is 4xx, the
// source's is 502, running out of time is 504. Whatever the answer, nothing
// was reserved, put or committed.
func TestAnImportFailureIsAnsweredByWhoseItIs(t *testing.T) {
	t.Parallel()

	timedOut := fmt.Errorf("%w: Get \"https://api.github.com/x\": %w", hub.ErrUnavailable, context.DeadlineExceeded)
	for name, tc := range map[string]struct {
		imp    *fakeImporter
		status int
		says   string
	}{
		"not a source":         {failing(fmt.Errorf("%w: a source is github:owner/repo/path@ref", hub.ErrInvalid)), http.StatusBadRequest, "a source is github:"},
		"a host not allowed":   {failing(fmt.Errorf("%w: https://evil.example is not an allowed source", hub.ErrRefused)), http.StatusBadRequest, "not an allowed source"},
		"nothing there":        {failing(fmt.Errorf("%w: GitHub has no such repository or ref", hub.ErrNotFound)), http.StatusUnprocessableEntity, "no such repository"},
		"over a limit":         {failing(errors.New(`"big.bin" is over 5 MiB`)), http.StatusUnprocessableEntity, "over 5 MiB"},
		"not a skill":          {importing(newSHA, skill.Entry{Path: "README.md", Data: []byte("# no")}), http.StatusUnprocessableEntity, "SKILL.md"},
		"the source is down":   {failing(fmt.Errorf("%w: GitHub answered 503", hub.ErrUnavailable)), http.StatusBadGateway, "GitHub answered 503"},
		"the budget ran out":   {failing(timedOut), http.StatusGatewayTimeout, "did not answer within 30s"},
		"the caller went away": {failing(fmt.Errorf("%w: %w", hub.ErrUnavailable, context.Canceled)), http.StatusBadGateway, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			s := newRouteStore()
			b := newBucket(s.txStore)
			rec := callWith(t, s, b, tc.imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID), importBody(t, pdfRef))

			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("status = %d (%s), want %d saying %q", rec.Code, rec.Body, tc.status, tc.says)
			}
			if s.touched() || len(b.objects) != 0 {
				t.Errorf("a failed import reached the store (%v) or the bucket (%d)", s.steps(), len(b.objects))
			}
		})
	}
}

// A timeout's own error names a URL and a Go error string; the caller is told
// the budget instead, which is what they can act on.
func TestATimeoutIsNotEchoed(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	imp := failing(fmt.Errorf("%w: Get \"https://api.github.com/repos/acme/skills/commits/main\": %w", hub.ErrUnavailable, context.DeadlineExceeded))
	rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID), importBody(t, pdfRef))

	if strings.Contains(rec.Body.String(), "api.github.com") || strings.Contains(rec.Body.String(), "deadline exceeded") {
		t.Errorf("body = %q, want only the budget", rec.Body)
	}
}

// The fetch gets the import budget and no more: the importer sees a deadline
// thirty seconds out, not the request's (which has none) and not a longer one.
func TestTheFetchIsGivenTheImportBudget(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	imp := importing(newSHA, pdfEntries("pdf")...)
	callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID), importBody(t, pdfRef))

	if imp.budget <= importBudget-time.Second || imp.budget > importBudget {
		t.Errorf("the fetch was given %s, want %s", imp.budget, importBudget)
	}
	if importBudget != 30*time.Second {
		t.Errorf("importBudget = %s; the spec promises 30s", importBudget)
	}
}

// The budget bounds the fetch and nothing after it: a fetch that uses most of
// it still leaves the reserve, put and commit a live context.
func TestTheWriteIsNotBoundByTheFetchBudget(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	b := newBucket(s.txStore)
	imp := importing(newSHA, pdfEntries("pdf")...)
	imp.delay = 50 * time.Millisecond

	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID), importBody(t, pdfRef))
	if rec.Code != http.StatusCreated || len(b.objects) != 1 {
		t.Errorf("status = %d (%s), %d objects", rec.Code, rec.Body, len(b.objects))
	}
}

// The server's WriteTimeout is as long as the import budget, and counts from
// when the request was read. An import that runs past it has to move its own
// write deadline, or the brain does all the work and the caller is sent a
// reset connection instead of the skill it now has.
func TestAnImportOutlivesTheServersWriteTimeout(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	imp := importing(newSHA, pdfEntries("pdf")...)
	imp.delay = 300 * time.Millisecond

	mux := http.NewServeMux()
	New(discardLogger(), s, newBucket(s.txStore), imp).Register(mux, api.NewGuard(skillRoutes(t), &fakeAuthz{allowed: true}, discardLogger()))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), memberOf(teamID))))
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	in := importBody(t, pdfRef)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+importPath(teamID), in.body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", in.contentType)

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("the caller got no answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", res.StatusCode)
	}
}

// The body is one object with one known field. Anything else is refused
// before a single request leaves the brain.
func TestAMalformedImportBodyFetchesNothing(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"not JSON":      `github:acme/skills/pdf`,
		"unknown field": `{"source":"github:acme/skills/pdf","ref":"main"}`,
		"two objects":   `{"source":"a"}{"source":"b"}`,
		"wrong type":    `{"source":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			s := newRouteStore()
			imp := importing(newSHA, pdfEntries("pdf")...)
			rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID),
				sent{"application/json", strings.NewReader(body)})

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(imp.fetched()) != 0 || s.touched() {
				t.Errorf("a malformed body fetched %q or reached the store", imp.fetched())
			}
		})
	}
}

// An empty source is still a source to judge, and the importer's judgement
// is the only one: the handler has no rules of its own to drift from it.
func TestAnEmptySourceIsJudgedByTheImporter(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	imp := failing(fmt.Errorf("%w: a source is github:owner/repo/path@ref", hub.ErrInvalid))
	rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, importPath(teamID), importBody(t, ""))

	if rec.Code != http.StatusBadRequest || !slices.Equal(imp.fetched(), []string{""}) {
		t.Errorf("status = %d, fetched %q", rec.Code, imp.fetched())
	}
}

func TestImportingATakenNameIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := callWith(t, s, b, importing(newSHA, pdfEntries("pdf")...), &fakeAuthz{allowed: true}, memberOf(row.TeamID),
		http.MethodPost, importPath(row.TeamID), importBody(t, pdfRef))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
	if len(s.skills) != 1 || s.skills[row.ID].Source != "upload" {
		t.Errorf("the existing skill changed: %+v", s.skills)
	}
}

func TestAFailedImportWriteIsAServerError(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	s.failAt = "InsertSkillFiles"
	rec := callWith(t, s, newBucket(s.txStore), importing(newSHA, pdfEntries("pdf")...), &fakeAuthz{allowed: true}, memberOf(teamID),
		http.MethodPost, importPath(teamID), importBody(t, pdfRef))

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "InsertSkillFiles") {
		t.Errorf("status = %d (%s), want a 500 that says nothing", rec.Code, rec.Body)
	}
	if len(s.skills) != 0 {
		t.Errorf("a skill was committed without its files: %+v", s.skills)
	}
}

// --- sync ---

// A moved source replaces the directory whole, under the same row and the
// same ref, with the new SHA; the old files' rows are gone.
func TestSyncReplacesASkillWhoseSourceMoved(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	imp := importing(newSHA, pdfEntries("pdf")...)

	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, syncPath(row.TeamID, row.ID), nothing)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}

	got := decodeDetail(t, rec)
	if got.ID != row.ID || got.Body != "# PDF v2\n" || got.SourceSHA == nil || *got.SourceSHA != newSHA ||
		got.SourceRef == nil || *got.SourceRef != pdfRef || got.Source != "github" {
		t.Errorf("synced = %+v", got)
	}
	if !slices.Equal(imp.fetched(), []string{pdfRef}) {
		t.Errorf("fetched %q, want the stored ref", imp.fetched())
	}
	var paths []string
	for _, f := range s.files[row.ID] {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{"scripts/fill.py"}) {
		t.Errorf("stored files = %v, want only the new directory's", paths)
	}
	if stored := s.skills[row.ID]; stored.SourceSha == nil || *stored.SourceSha != newSHA {
		t.Errorf("stored sha = %v", stored.SourceSha)
	}
}

// An unchanged source writes nothing and is not validated again: the entries
// here would not assemble, and the answer is still the skill as stored. A
// sync that found nothing new must not break a skill the rules have since
// grown stricter about.
func TestSyncOfAnUnchangedSourceWritesNothing(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	objectsBefore := len(b.objects)
	imp := importing(oldSHA, skill.Entry{Path: "README.md", Data: []byte("not a skill")})

	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, syncPath(row.TeamID, row.ID), nothing)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if steps := s.steps(); len(steps) != 0 || len(b.objects) != objectsBefore {
		t.Errorf("an unchanged sync wrote: %v, objects %d -> %d", steps, objectsBefore, len(b.objects))
	}

	got := decodeDetail(t, rec)
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
	}
	if got.Body != "# PDF\n" || !slices.Equal(paths, []string{"references/FORMS.md", "scripts/fill.py"}) {
		t.Errorf("answered %q with %v, want the stored skill", got.Body, paths)
	}
}

func TestSyncOfAnUnchangedSourceThatCannotListItsFilesIsAServerError(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	s.filesErr = errAny
	rec := callWith(t, s, b, importing(oldSHA), &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, syncPath(row.TeamID, row.ID), nothing)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), errAny.Error()) {
		t.Errorf("status = %d (%s), want a 500 that says nothing", rec.Code, rec.Body)
	}
}

// An uploaded skill has no source, so there is nothing to fetch — and nothing
// is fetched.
func TestSyncOfAnUploadedSkillIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	imp := importing(newSHA, pdfEntries("pdf")...)
	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, syncPath(row.TeamID, row.ID), nothing)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no source") {
		t.Errorf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
	if len(imp.fetched()) != 0 || s.wrote() {
		t.Errorf("an uploaded skill was fetched (%q) or written (%v)", imp.fetched(), s.steps())
	}
}

// A sync that fails leaves the skill as it was, and answers as an import
// would.
func TestAFailedSyncKeepsTheSkill(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		imp    *fakeImporter
		status int
	}{
		"the source is down": {failing(fmt.Errorf("%w: GitHub answered 503", hub.ErrUnavailable)), http.StatusBadGateway},
		"the ref is gone":    {failing(fmt.Errorf("%w: no such ref", hub.ErrNotFound)), http.StatusUnprocessableEntity},
		"no longer a skill":  {importing(newSHA, skill.Entry{Path: "README.md", Data: []byte("# gone")}), http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, githubOrigin)
			rec := callWith(t, s, b, tc.imp, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, syncPath(row.TeamID, row.ID), nothing)

			if rec.Code != tc.status {
				t.Errorf("status = %d (%s), want %d", rec.Code, rec.Body, tc.status)
			}
			if s.wrote() || *s.skills[row.ID].SourceSha != oldSHA || len(s.files[row.ID]) != 2 {
				t.Errorf("a failed sync changed the skill: %v", s.steps())
			}
		})
	}
}

// Upstream renamed the skill to one the team already has.
func TestSyncToATakenNameIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	other := aSkill(row.TeamID, "docx")
	s.skills[other.ID] = other

	rec := callWith(t, s, b, importing(newSHA, pdfEntries("docx")...), &fakeAuthz{allowed: true}, memberOf(row.TeamID),
		http.MethodPost, syncPath(row.TeamID, row.ID), nothing)
	if rec.Code != http.StatusConflict || s.skills[row.ID].Name != "pdf" {
		t.Errorf("status = %d (%s), name %q", rec.Code, rec.Body, s.skills[row.ID].Name)
	}
}

// The skill was deleted while its source was being fetched.
func TestSyncOfASkillDeletedMidFetchIsNotFound(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	s.vanishOnRead = true
	rec := callWith(t, s, b, importing(newSHA, pdfEntries("pdf")...), &fakeAuthz{allowed: true}, memberOf(row.TeamID),
		http.MethodPost, syncPath(row.TeamID, row.ID), nothing)

	if rec.Code != http.StatusNotFound || len(s.skills) != 0 {
		t.Errorf("status = %d (%s), skills %v", rec.Code, rec.Body, s.skills)
	}
}

func TestAFailedSyncWriteIsAServerError(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	s.failAt = "ClearSkillFiles"
	rec := callWith(t, s, b, importing(newSHA, pdfEntries("pdf")...), &fakeAuthz{allowed: true}, memberOf(row.TeamID),
		http.MethodPost, syncPath(row.TeamID, row.ID), nothing)

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "ClearSkillFiles") {
		t.Errorf("status = %d (%s), want a 500 that says nothing", rec.Code, rec.Body)
	}
	if *s.skills[row.ID].SourceSha != oldSHA {
		t.Error("a failed sync committed the new SHA")
	}
}

// Another team's skill is not found, and its source is never fetched: a sync
// would otherwise let anyone make the brain fetch any team's private ref.
func TestSyncOfAnotherTeamsSkillFetchesNothing(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, githubOrigin)
	mine := uuid.New()
	imp := importing(newSHA, pdfEntries("pdf")...)
	rec := callWith(t, s, b, imp, &fakeAuthz{allowed: true}, memberOf(mine), http.MethodPost, syncPath(mine, row.ID), nothing)

	if rec.Code != http.StatusNotFound || len(imp.fetched()) != 0 || s.wrote() {
		t.Errorf("status = %d, fetched %q, wrote %v", rec.Code, imp.fetched(), s.steps())
	}
}

func TestSyncNeedsASkillID(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	imp := importing(newSHA)
	rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost,
		collection(teamID)+"/not-a-uuid/sync", nothing)

	if rec.Code != http.StatusBadRequest || len(imp.fetched()) != 0 {
		t.Errorf("status = %d, fetched %q", rec.Code, imp.fetched())
	}
}

// --- authorisation and contract ---

// Both routes write, so both need skill:write, and a refused caller makes the
// brain fetch nothing: the import route is an outbound request on a caller's
// say-so, and a refusal must not still make it.
func TestImportAndSyncNeedSkillWrite(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		path func(db.Skill) string
		body func(*testing.T) sent
		ok   int
	}{
		"import": {func(r db.Skill) string { return importPath(r.TeamID) }, func(t *testing.T) sent { return importBody(t, pdfRef) }, http.StatusCreated},
		"sync":   {func(r db.Skill) string { return syncPath(r.TeamID, r.ID) }, func(*testing.T) sent { return nothing }, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, allowed := range []bool{true, false} {
				s, b, row := seeded(t, githubOrigin)
				imp := importing(newSHA, pdfEntries("docx")...)
				a := &fakeAuthz{allowed: allowed}
				rec := callWith(t, s, b, imp, a, memberOf(row.TeamID), http.MethodPost, tc.path(row), tc.body(t))

				want := tc.ok
				if !allowed {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Errorf("allowed=%v: status %d, want %d (%s)", allowed, rec.Code, want, rec.Body)
				}
				if len(a.asked) != 1 || a.asked[0] != authz.Action("skill:write") {
					t.Errorf("the guard asked for %v, want skill:write", a.asked)
				}
				if !allowed && (len(imp.fetched()) != 0 || s.touched()) {
					t.Errorf("a refused caller fetched %q or reached the store", imp.fetched())
				}
			}
		})
	}
}

func TestImportAndSyncRefuseARequestWithNoUser(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for name, path := range map[string]string{"import": importPath(teamID), "sync": syncPath(teamID, id)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newRouteStore()
			imp := importing(newSHA, pdfEntries("pdf")...)
			rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, nil, http.MethodPost, path, importBody(t, pdfRef))

			if rec.Code != http.StatusInternalServerError || len(imp.fetched()) != 0 || s.touched() {
				t.Errorf("status = %d, fetched %q", rec.Code, imp.fetched())
			}
		})
	}
}

// Sync answers POST alone. Import is a literal segment beside {skillID}, so
// another verb on it lands on the skill routes with "import" as the id, and
// is refused there as not a uuid — a refusal, never a fetch or a write.
func TestImportAndSyncAnswerOnlyPOST(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, syncPath(teamID, id), http.StatusMethodNotAllowed},
		{http.MethodPut, syncPath(teamID, id), http.StatusMethodNotAllowed},
		{http.MethodGet, importPath(teamID), http.StatusBadRequest},
		{http.MethodDelete, importPath(teamID), http.StatusBadRequest},
		{http.MethodPut, importPath(teamID), http.StatusBadRequest},
	} {
		s := newRouteStore()
		imp := importing(newSHA)
		rec := callWith(t, s, newBucket(s.txStore), imp, &fakeAuthz{allowed: true}, memberOf(teamID), tc.method, tc.path, skillNamed(t, "pdf"))
		if rec.Code != tc.status || len(imp.fetched()) != 0 || s.touched() {
			t.Errorf("%s %s: status %d, want %d; fetched %q", tc.method, tc.path, rec.Code, tc.status, imp.fetched())
		}
	}
}

// An import answers with the same detail as a create, origin included.
func TestAnImportAnswersOnlyContractedFields(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	rec := callWith(t, s, newBucket(s.txStore), importing(newSHA, pdfEntries("pdf")...), &fakeAuthz{allowed: true}, memberOf(teamID),
		http.MethodPost, importPath(teamID), importBody(t, pdfRef))

	wantKeys(t, "import", keysOf(t, rec.Body.Bytes()),
		append(slices.Clone(summaryKeys), "metadata", "source_ref", "source_sha", "body", "files")...)
}

// The source's failures are logged, because they are the operator's to see —
// a rate limit, a revoked token, an outage — and the caller's are not, or a
// typo in a source would page somebody.
func TestOnlyTheSourcesFailuresAreLogged(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err    error
		logged bool
	}{
		"rate limited": {fmt.Errorf("%w: GitHub refused the request (429)", hub.ErrUnavailable), true},
		"timed out":    {fmt.Errorf("%w: %w", hub.ErrUnavailable, context.DeadlineExceeded), true},
		"a typo":       {fmt.Errorf("%w: not a source", hub.ErrInvalid), false},
		"not found":    {fmt.Errorf("%w: no such ref", hub.ErrNotFound), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			teamID := uuid.New()
			s := newRouteStore()

			mux := http.NewServeMux()
			New(logger, s, newBucket(s.txStore), failing(tc.err)).Register(mux, api.NewGuard(skillRoutes(t), &fakeAuthz{allowed: true}, logger))
			in := importBody(t, pdfRef)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, importPath(teamID), in.body)
			req.Header.Set("Content-Type", in.contentType)
			mux.ServeHTTP(httptest.NewRecorder(), req.WithContext(auth.WithUser(req.Context(), memberOf(teamID))))

			if got := strings.Contains(logs.String(), "skill import failed"); got != tc.logged {
				t.Errorf("logged = %v, want %v:\n%s", got, tc.logged, logs.String())
			}
		})
	}
}
