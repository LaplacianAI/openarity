package skills

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- fakes ---

// errAny stands in for any failure of ours. Which one never changes the
// answer: the caller is told nothing and the detail goes to the log.
var errAny = errors.New("connection reset by peer")

// routeStore is the txStore the writer uses, plus the reads the routes make.
// The list pages as the real query does — newest first, (created_at, id) as
// the tiebreak — so the cursor tests check the handler, not the fake.
type routeStore struct {
	*txStore

	readErr, filesErr, deleteErr error
	vanishOnRead                 bool

	reads    int
	deleted  []uuid.UUID
	listArgs []db.ListSkillsByTeamParams
	fileArgs []db.GetSkillFileParams
}

func newRouteStore(existing ...db.Skill) *routeStore {
	return &routeStore{txStore: newTxStore(existing...)}
}

func (s *routeStore) touched() bool {
	return s.reads != 0 || len(s.listArgs) != 0 || len(s.fileArgs) != 0 || s.wrote()
}

func (s *routeStore) wrote() bool {
	return len(s.deleted) != 0 || len(s.steps()) != 0
}

func (s *routeStore) GetSkill(_ context.Context, id uuid.UUID) (db.Skill, error) {
	s.reads++
	if s.readErr != nil {
		return db.Skill{}, s.readErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.skills[id]
	if !ok {
		return db.Skill{}, pgx.ErrNoRows
	}
	if s.vanishOnRead {
		delete(s.skills, id)
		delete(s.files, id)
	}
	return row, nil
}

func (s *routeStore) ListSkillsByTeam(_ context.Context, arg db.ListSkillsByTeamParams) ([]db.ListSkillsByTeamRow, error) {
	s.listArgs = append(s.listArgs, arg)
	if s.readErr != nil {
		return nil, s.readErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []db.Skill
	for _, row := range s.skills {
		if row.TeamID != arg.TeamID {
			continue
		}
		if arg.UseCursor && !before(row, arg.AfterCreatedAt, arg.AfterID) {
			continue
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b db.Skill) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	})

	out := make([]db.ListSkillsByTeamRow, 0, len(rows))
	for _, row := range rows[:min(len(rows), int(arg.PageSize))] {
		out = append(out, db.ListSkillsByTeamRow{
			ID: row.ID, TeamID: row.TeamID, Name: row.Name, Description: row.Description,
			Source: row.Source, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

// before is (created_at, id) < (at, id), as the query compares rows.
func before(row db.Skill, at time.Time, id uuid.UUID) bool {
	if c := row.CreatedAt.Compare(at); c != 0 {
		return c < 0
	}
	return row.ID.String() < id.String()
}

func (s *routeStore) ListSkillFiles(_ context.Context, skillID uuid.UUID) ([]db.SkillFile, error) {
	s.reads++
	if s.filesErr != nil {
		return nil, s.filesErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.SkillFile, 0, len(s.files[skillID]))
	for _, f := range s.files[skillID] {
		out = append(out, asRow(f))
	}
	slices.SortFunc(out, func(a, b db.SkillFile) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

func (s *routeStore) GetSkillFile(_ context.Context, arg db.GetSkillFileParams) (db.SkillFile, error) {
	s.fileArgs = append(s.fileArgs, arg)
	if s.filesErr != nil {
		return db.SkillFile{}, s.filesErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files[arg.SkillID] {
		if f.Path == arg.Path {
			return asRow(f), nil
		}
	}
	return db.SkillFile{}, pgx.ErrNoRows
}

func (s *routeStore) DeleteSkill(_ context.Context, id uuid.UUID) error {
	s.deleted = append(s.deleted, id)
	if s.deleteErr != nil {
		return s.deleteErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skills, id)
	delete(s.files, id)
	return nil
}

func asRow(f db.InsertSkillFilesParams) db.SkillFile {
	return db.SkillFile{
		SkillID: f.SkillID, TeamID: f.TeamID, Path: f.Path, Size: f.Size,
		Sha256: f.Sha256, MediaType: f.MediaType, ObjectKey: f.ObjectKey,
	}
}

type fakeAuthz struct {
	allowed bool
	asked   []authz.Action
}

func (*fakeAuthz) IsSuperAdmin(*auth.User) bool { return false }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return f.allowed, nil
}

// No route in this package is any_team, so reaching the strictly weaker check
// is itself the failure.
func (*fakeAuthz) CanInAnyTeam(context.Context, *auth.User, authz.Action) (bool, error) {
	panic("a skills route used the strictly weaker CanInAnyTeam")
}

// skillRoutes is what rbac.json maps for this package. The real guard, not an
// open one, so a route whose scope changes fails here as well as in
// internal/store.
func skillRoutes(t *testing.T) authz.Routes {
	t.Helper()

	rs := authz.NewRoutes()
	add := func(method, path, scope string, permission *string) {
		t.Helper()
		if err := rs.Add(method, path, scope, permission); err != nil {
			t.Fatalf("Add %s %s: %v", method, path, err)
		}
	}

	write := "skill:write"
	add("GET", "/teams/{id}/skills", "member", nil)
	add("POST", "/teams/{id}/skills", "team", &write)
	add("GET", "/teams/{id}/skills/{skillID}", "member", nil)
	add("PUT", "/teams/{id}/skills/{skillID}", "team", &write)
	add("DELETE", "/teams/{id}/skills/{skillID}", "team", &write)
	add("GET", "/teams/{id}/skills/{skillID}/files/{path...}", "member", nil)
	return rs
}

// sent is a request body and the content type it claims.
type sent struct {
	contentType string
	body        io.Reader
}

var nothing = sent{}

func call(t *testing.T, s *routeStore, b *bucket, a *fakeAuthz, u *auth.User, method, path string, in sent) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s, b).Register(mux, api.NewGuard(skillRoutes(t), a, discardLogger()))

	req := httptest.NewRequestWithContext(t.Context(), method, path, in.body)
	if in.contentType != "" {
		req.Header.Set("Content-Type", in.contentType)
	}
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(teamID uuid.UUID) *auth.User {
	return &auth.User{
		ID: uuid.New(), Issuer: "dev", Subject: "someone",
		Teams: []auth.Membership{{TeamID: teamID, Name: "platform", Role: "member"}},
	}
}

func outsider() *auth.User {
	return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
}

func pgCode(code string) error { return &pgconn.PgError{Code: code} }

func aSkill(teamID uuid.UUID, name string) db.Skill {
	return db.Skill{
		ID: uuid.New(), TeamID: teamID, Name: name, Description: "Does " + name,
		Metadata: []byte("{}"), Body: "# " + name, Source: "upload",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// skillNamed is an upload of a skill with one file beside its SKILL.md.
func skillNamed(t *testing.T, name string) sent {
	t.Helper()

	ct, body := multipartBody(t,
		part{"files", "SKILL.md", "---\nname: " + name + "\ndescription: Does " + name + ".\n---\n# " + name + "\n"},
		part{"files", "scripts/run.py", "print('" + name + "')\n"},
	)
	return sent{ct, body}
}

// seeded is a store holding the pdf skill, written the way a create writes
// it, with its step log cleared so a test sees only its own request.
func seeded(t *testing.T, o Origin) (*routeStore, *bucket, db.Skill) {
	t.Helper()

	s := newRouteStore()
	b := newBucket(s.txStore)
	row, err := writer{store: s, objects: b}.create(t.Context(), uuid.New(), pdfSkill(t), o)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.log = nil
	return s, b, row
}

func collection(teamID uuid.UUID) string { return "/teams/" + teamID.String() + "/skills" }

func one(teamID, id uuid.UUID) string { return collection(teamID) + "/" + id.String() }

func fileAt(teamID, id uuid.UUID, path string) string { return one(teamID, id) + "/files/" + path }

// --- authorisation ---

// Every write asks the guard for skill:write and nothing else, and a refusal
// reaches neither the store nor the bucket.
func TestWritesNeedSkillWrite(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method string
		path   func(db.Skill) string
		body   func(*testing.T) sent
		ok     int
	}{
		"create":  {http.MethodPost, func(r db.Skill) string { return collection(r.TeamID) }, func(t *testing.T) sent { return skillNamed(t, "docx") }, http.StatusCreated},
		"replace": {http.MethodPut, func(r db.Skill) string { return one(r.TeamID, r.ID) }, func(t *testing.T) sent { return skillNamed(t, "pdf") }, http.StatusOK},
		"delete":  {http.MethodDelete, func(r db.Skill) string { return one(r.TeamID, r.ID) }, func(*testing.T) sent { return nothing }, http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, allowed := range []bool{true, false} {
				s, b, row := seeded(t, uploaded)
				objectsBefore := len(b.objects)
				a := &fakeAuthz{allowed: allowed}
				rec := call(t, s, b, a, memberOf(row.TeamID), tc.method, tc.path(row), tc.body(t))

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
				if !allowed && (s.touched() || len(b.objects) != objectsBefore) {
					t.Errorf("a refused write reached the store (%v) or the bucket", s.steps())
				}
			}
		})
	}
}

// Reading is member-scoped: belonging is the whole check, so no permission is
// asked for, and an outsider gets 404 — a 403 would confirm the team exists.
func TestReadsAreForMembersAndHiddenFromOutsiders(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]func(db.Skill) string{
		"list": func(r db.Skill) string { return collection(r.TeamID) },
		"get":  func(r db.Skill) string { return one(r.TeamID, r.ID) },
		"file": func(r db.Skill) string { return fileAt(r.TeamID, r.ID, "scripts/fill.py") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, uploaded)
			a := &fakeAuthz{}
			if rec := call(t, s, b, a, memberOf(row.TeamID), http.MethodGet, path(row), nothing); rec.Code != http.StatusOK {
				t.Errorf("as a member: %d, want 200 (%s)", rec.Code, rec.Body)
			}
			if len(a.asked) != 0 {
				t.Errorf("a read asked for %v; belonging should be the whole check", a.asked)
			}

			s, b, row = seeded(t, uploaded)
			if rec := call(t, s, b, a, outsider(), http.MethodGet, path(row), nothing); rec.Code != http.StatusNotFound {
				t.Errorf("as an outsider: %d, want 404", rec.Code)
			}
			if s.touched() {
				t.Error("an outsider's read reached the store")
			}
		})
	}
}

// Without the middleware there is no user on the context. That is a wiring
// bug, so it fails loudly rather than serving an anonymous caller.
func TestEveryRouteRefusesARequestWithNoUser(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"list":    {http.MethodGet, collection(teamID)},
		"create":  {http.MethodPost, collection(teamID)},
		"get":     {http.MethodGet, one(teamID, id)},
		"replace": {http.MethodPut, one(teamID, id)},
		"delete":  {http.MethodDelete, one(teamID, id)},
		"file":    {http.MethodGet, fileAt(teamID, id, "scripts/fill.py")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newRouteStore()
			rec := call(t, s, newBucket(s.txStore), &fakeAuthz{allowed: true}, nil, tc.method, tc.path, skillNamed(t, "pdf"))

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if s.touched() {
				t.Error("the handler reached the store without a user")
			}
		})
	}
}

// The verbs are part of the contract. A route answering a verb it never
// declared is how a read endpoint quietly becomes a write one — a file most of
// all, which has no write route at all.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	teamID, id := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path string }{
		"PUT on the collection":    {http.MethodPut, collection(teamID)},
		"DELETE on the collection": {http.MethodDelete, collection(teamID)},
		"POST on one skill":        {http.MethodPost, one(teamID, id)},
		"PATCH on one skill":       {http.MethodPatch, one(teamID, id)},
		"PUT on a file":            {http.MethodPut, fileAt(teamID, id, "a.md")},
		"DELETE on a file":         {http.MethodDelete, fileAt(teamID, id, "a.md")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newRouteStore()
			rec := call(t, s, newBucket(s.txStore), &fakeAuthz{allowed: true}, memberOf(teamID), tc.method, tc.path, nothing)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if s.touched() {
				t.Error("an undeclared method reached the store")
			}
		})
	}
}

// --- the wire contract ---

func keysOf(t *testing.T, raw []byte) map[string]bool {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("body is not an object: %v (%s)", err, raw)
	}
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	return keys
}

func wantKeys(t *testing.T, what string, got map[string]bool, want ...string) {
	t.Helper()

	for _, k := range want {
		if !got[k] {
			t.Errorf("%s: field %q is missing", what, k)
		}
		delete(got, k)
	}
	for k := range got {
		t.Errorf("%s: unexpected field %q", what, k)
	}
}

var summaryKeys = []string{"id", "team_id", "name", "description", "source", "created_at", "updated_at"}

// The wire shape is a contract. This fails when a struct grows a field, and
// holds the two absences that matter: no body in a list, and no optional
// field the manifest did not set.
func TestOnlyContractedFieldsAreSerialised(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	detailKeys := append(slices.Clone(summaryKeys), "license", "metadata", "body", "files")

	rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nothing)
	wantKeys(t, "get", keysOf(t, rec.Body.Bytes()), detailKeys...)

	var got struct {
		Files []json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Files) == 0 {
		t.Fatalf("get has no files: %v (%s)", err, rec.Body)
	}
	for _, f := range got.Files {
		wantKeys(t, "file", keysOf(t, f), "path", "size", "sha256", "media_type")
	}

	rec = call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, collection(row.TeamID), skillNamed(t, "docx"))
	wantKeys(t, "create", keysOf(t, rec.Body.Bytes()), append(slices.Clone(summaryKeys), "metadata", "body", "files")...)

	rec = call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, collection(row.TeamID), nothing)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("list body is not a page with items: %v (%s)", err, rec.Body)
	}
	for _, item := range page.Items {
		wantKeys(t, "list item", keysOf(t, item), summaryKeys...)
	}
}

// An imported skill says where it came from, exactly, so a reader can tell
// the team's own skills from a copy of somebody else's.
func TestAnImportedSkillShowsWhereItCameFrom(t *testing.T) {
	t.Parallel()

	gh := Origin{Source: "github", Ref: text("github:anthropics/skills/pdf@main"), SHA: text("0123abc")}
	s, b, row := seeded(t, gh)

	rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nothing)
	var got detail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if got.Source != "github" || got.SourceRef == nil || *got.SourceRef != *gh.Ref || got.SourceSHA == nil || *got.SourceSHA != *gh.SHA {
		t.Errorf("origin = %s %v %v", got.Source, got.SourceRef, got.SourceSHA)
	}
}

// --- create ---

func decodeDetail(t *testing.T, rec *httptest.ResponseRecorder) detail {
	t.Helper()

	var got detail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v (%s)", err, rec.Body)
	}
	return got
}

// What comes back describes what was stored: the manifest's fields, and each
// file with the size and hash of the bytes now in the bucket.
func TestCreateStoresTheUploadAndDescribesIt(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	b := newBucket(s.txStore)

	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), skillNamed(t, "docx"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}

	got := decodeDetail(t, rec)
	if got.Name != "docx" || got.Description != "Does docx." || got.Body != "# docx\n" || got.Source != "upload" || got.TeamID != teamID {
		t.Errorf("created = %+v", got)
	}
	if string(got.Metadata) != "{}" {
		t.Errorf("metadata = %s, want {}", got.Metadata)
	}

	want := []byte("print('docx')\n")
	sum := sha256.Sum256(want)
	if len(got.Files) != 1 || got.Files[0].Path != "scripts/run.py" || got.Files[0].Size != int64(len(want)) ||
		got.Files[0].SHA256 != hex.EncodeToString(sum[:]) || !strings.HasPrefix(got.Files[0].MediaType, "text/plain") {
		t.Errorf("files = %+v", got.Files)
	}

	rows := s.files[got.ID]
	if len(rows) != 1 || !bytes.Equal(b.objects[rows[0].ObjectKey], want) {
		t.Errorf("stored %+v; the bucket does not hold the file under its key", rows)
	}
}

func TestCreateTakesAZip(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	body := zipBody(t,
		part{filename: "pdf/SKILL.md", data: uploadManifest},
		part{filename: "pdf/scripts/fill.py", data: "print('fill')\n"},
	)

	rec := call(t, s, newBucket(s.txStore), &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), sent{"application/zip", body})
	if got := decodeDetail(t, rec); rec.Code != http.StatusCreated || got.Name != "pdf" || len(got.Files) != 1 {
		t.Errorf("status %d, created %+v", rec.Code, got)
	}
}

// An upload refused for its shape or content never reserves a key, puts an
// object or opens a transaction.
func TestARefusedUploadNeverReachesTheStore(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ct, noManifest := multipartBody(t, part{"files", "README.md", "# nope"})
	for name, tc := range map[string]struct {
		in     sent
		status int
	}{
		"JSON":         {sent{"application/json", strings.NewReader(`{"name":"pdf"}`)}, http.StatusUnsupportedMediaType},
		"not a skill":  {sent{ct, noManifest}, http.StatusBadRequest},
		"not a zip":    {sent{"application/zip", strings.NewReader("PK?")}, http.StatusBadRequest},
		"past the cap": {sent{"application/zip", &endless{}}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newRouteStore()
			b := newBucket(s.txStore)
			rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(teamID), http.MethodPost, collection(teamID), tc.in)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			if s.touched() || len(b.objects) != 0 {
				t.Errorf("a refused upload reached the store (%v) or the bucket (%d)", s.steps(), len(b.objects))
			}
		})
	}
}

func TestADuplicateNameIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPost, collection(row.TeamID), skillNamed(t, "pdf"))

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a skill with that name already exists") {
		t.Errorf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
	if len(s.skills) != 1 {
		t.Errorf("%d skills after a conflict, want 1", len(s.skills))
	}
}

// --- get ---

// A skill is read with the files beside it, in path order, each described by
// what is stored rather than recomputed.
func TestGetListsTheFilesBesideTheManifest(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nothing)
	got := decodeDetail(t, rec)

	if got.ID != row.ID || got.Body != "# PDF\n" || got.License == nil || *got.License != "MIT" || string(got.Metadata) != `{"author":"ops"}` {
		t.Errorf("got = %+v", got)
	}
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
		stored := slices.IndexFunc(s.files[row.ID], func(r db.InsertSkillFilesParams) bool { return r.Path == f.Path })
		if stored < 0 || hex.EncodeToString(s.files[row.ID][stored].Sha256) != f.SHA256 {
			t.Errorf("%s: sha256 %s is not the stored one", f.Path, f.SHA256)
		}
	}
	if want := []string{"references/FORMS.md", "scripts/fill.py"}; !slices.Equal(paths, want) {
		t.Errorf("files = %v, want %v", paths, want)
	}
}

// A skill with no files lists [] — "we looked, there are none" — not null.
func TestASkillWithNoFilesListsAnEmptyArray(t *testing.T) {
	t.Parallel()

	row := aSkill(uuid.New(), "bare")
	s := newRouteStore(row)
	rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, one(row.TeamID, row.ID), nothing)

	if !strings.Contains(rec.Body.String(), `"files":[]`) {
		t.Errorf("body = %s, want files to be []", rec.Body)
	}
}

// Every route that names a skill answers 404 for another team's, and changes
// nothing: a refusal that still wrote would be invisible in the status.
func TestAnotherTeamsSkillIsNotFound(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method string
		path   func(team uuid.UUID, row db.Skill) string
		body   func(*testing.T) sent
	}{
		"get":     {http.MethodGet, func(team uuid.UUID, r db.Skill) string { return one(team, r.ID) }, nil},
		"replace": {http.MethodPut, func(team uuid.UUID, r db.Skill) string { return one(team, r.ID) }, func(t *testing.T) sent { return skillNamed(t, "pdf") }},
		"delete":  {http.MethodDelete, func(team uuid.UUID, r db.Skill) string { return one(team, r.ID) }, nil},
		"file":    {http.MethodGet, func(team uuid.UUID, r db.Skill) string { return fileAt(team, r.ID, "scripts/fill.py") }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, uploaded)
			mine := uuid.New()
			in := nothing
			if tc.body != nil {
				in = tc.body(t)
			}

			rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(mine), tc.method, tc.path(mine, row), in)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body)
			}
			if s.wrote() || len(s.fileArgs) != 0 {
				t.Errorf("another team's skill was touched: steps %v, deleted %v, files %v", s.steps(), s.deleted, s.fileArgs)
			}
		})
	}
}

func TestAMissingSkillIsNotFound(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	for _, path := range []string{one(teamID, uuid.New()), fileAt(teamID, uuid.New(), "a.md")} {
		if rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, path, nothing); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestASkillIDThatIsNotAUUIDIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	for _, path := range []string{collection(teamID) + "/pdf", collection(teamID) + "/pdf/files/a.md"} {
		rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, path, nothing)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "skill id must be a uuid") {
			t.Errorf("%s: status = %d (%s), want 400", path, rec.Code, rec.Body)
		}
	}
	if s.touched() {
		t.Error("a malformed id reached the store")
	}
}

// --- replace ---

func TestReplaceSwapsTheWholeDirectory(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), skillNamed(t, "pdf-v2"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	got := decodeDetail(t, rec)
	if got.ID != row.ID || got.Name != "pdf-v2" || len(got.Files) != 1 || got.Files[0].Path != "scripts/run.py" {
		t.Errorf("replaced = %+v", got)
	}
	if paths := fileKeys(s.files[row.ID]); len(paths) != 1 || !strings.HasPrefix(paths[0], "scripts/run.py=") {
		t.Errorf("stored files = %v, want only the new one", paths)
	}
}

// An upload over an imported skill makes it the team's own: what came back
// says so, and the old origin is gone rather than left describing a skill it
// no longer describes.
func TestReplacingAnImportThroughTheAPIMakesItAnUpload(t *testing.T) {
	t.Parallel()

	gh := Origin{Source: "github", Ref: text("github:anthropics/skills/pdf@main"), SHA: text("0123abc")}
	s, b, row := seeded(t, gh)

	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), skillNamed(t, "pdf"))
	got := decodeDetail(t, rec)
	if got.Source != "upload" || got.SourceRef != nil || got.SourceSHA != nil {
		t.Errorf("origin after upload = %s %v %v", got.Source, got.SourceRef, got.SourceSHA)
	}
	if stored := s.skills[row.ID]; stored.Source != "upload" || stored.SourceRef != nil {
		t.Errorf("stored origin = %s %v", stored.Source, stored.SourceRef)
	}
}

// A bad upload is refused before the skill is read, so a replace with a
// broken body says nothing about whether the id exists.
func TestABadReplaceUploadNeverReadsTheSkill(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, uuid.New()),
		sent{"application/json", strings.NewReader("{}")})

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
	if s.touched() {
		t.Error("a bad replace read the skill")
	}
}

// Deleted between the read and the write: 404, and nothing resurrected.
func TestReplacingASkillDeletedMeanwhileIsNotFound(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	s.vanishOnRead = true

	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodPut, one(row.TeamID, row.ID), skillNamed(t, "pdf"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d (%s), want 404", rec.Code, rec.Body)
	}
	if len(s.skills) != 0 {
		t.Errorf("the deleted skill came back: %v", s.skills)
	}
}

func TestReplacingIntoATakenNameIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, pdf := seeded(t, uploaded)
	docx := aSkill(pdf.TeamID, "docx")
	s.skills[docx.ID] = docx

	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(pdf.TeamID), http.MethodPut, one(pdf.TeamID, docx.ID), skillNamed(t, "pdf"))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
	if s.skills[docx.ID].Name != "docx" {
		t.Errorf("the skill was renamed anyway: %+v", s.skills[docx.ID])
	}
}

// --- delete ---

func TestDeleteRemovesTheSkill(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodDelete, one(row.TeamID, row.ID), nothing)

	if rec.Code != http.StatusNoContent || !slices.Equal(s.deleted, []uuid.UUID{row.ID}) {
		t.Errorf("status %d, deleted %v", rec.Code, s.deleted)
	}
}

func TestDeletingAGrantedSkillIsAConflict(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	s.deleteErr = pgCode(codeRestrictViolation)

	rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), http.MethodDelete, one(row.TeamID, row.ID), nothing)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "granted to an agent") {
		t.Errorf("status = %d (%s), want 409", rec.Code, rec.Body)
	}
}

// --- files ---

// The headers are the security control on this route. Asserted on what left
// the process — rec.Result() — never on the recorder's live map, which keeps
// headers set too late to reach the wire.
func TestAFileIsServedAsItsRecordedTypeAndAlwaysDownloads(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, fileAt(row.TeamID, row.ID, "scripts/fill.py"), nothing)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)

	stored := s.files[row.ID][slices.IndexFunc(s.files[row.ID], func(r db.InsertSkillFilesParams) bool { return r.Path == "scripts/fill.py" })]
	if res.StatusCode != http.StatusOK || string(body) != "print('fill')\n" {
		t.Fatalf("status %d, body %q", res.StatusCode, body)
	}
	for header, want := range map[string]string{
		"Content-Type":           stored.MediaType,
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    `attachment; filename=fill.py`,
		"Content-Length":         "14",
		"Cache-Control":          "private, no-store",
	} {
		if got := res.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// Even a type that would render inline as an attachment downloads here: a
// skill's files are for an agent, not for a browser.
func TestAFileThatWouldRenderStillDownloads(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	b := newBucket(s.txStore)
	dir := directory(t,
		skillEntry("SKILL.md", uploadManifest),
		skillEntry("assets/logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
	)
	row, err := writer{store: s, objects: b}.create(t.Context(), teamID, dir, uploaded)
	if err != nil {
		t.Fatal(err)
	}

	res := call(t, s, b, &fakeAuthz{}, memberOf(teamID), http.MethodGet, fileAt(teamID, row.ID, "assets/logo.png"), nothing).Result()
	if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("a png was served as %q, %q", res.Header.Get("Content-Type"), got)
	}
}

// {path...} keeps the folders: the lookup is the whole path, not its last
// segment, so two files with one name in different folders stay apart.
func TestAFilesPathKeepsItsFolders(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, fileAt(row.TeamID, row.ID, "references/FORMS.md"), nothing)

	if len(s.fileArgs) != 1 || s.fileArgs[0].Path != "references/FORMS.md" || s.fileArgs[0].SkillID != row.ID {
		t.Errorf("looked up %+v, want references/FORMS.md in skill %s", s.fileArgs, row.ID)
	}
}

// SKILL.md is the skill's row, not one of its files, and a path the skill does
// not hold is simply absent.
func TestAFileTheSkillDoesNotHoldIsNotFound(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"SKILL.md", "scripts/missing.py", "scripts", "FILL.PY"} {
		t.Run(p, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, uploaded)
			if rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, fileAt(row.TeamID, row.ID, p), nothing); rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

// {path...} is decoded, so %ff arrives as invalid UTF-8 and %00 as a NUL.
// Postgres refuses both with an error the handler would answer as a 500;
// neither can name a stored file, so each is a 404 that never asks.
func TestAPathPostgresCannotHoldIsNotFoundWithoutAQuery(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"fill%ff.py", "fill%00.py"} {
		t.Run(p, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, uploaded)
			rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, fileAt(row.TeamID, row.ID, p), nothing)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if len(s.fileArgs) != 0 {
				t.Errorf("asked Postgres for %q", s.fileArgs[0].Path)
			}
		})
	}
}

// A row whose object is gone is our fault, not the caller's: 500, and the
// detail goes to the log.
func TestAFileWhoseObjectIsMissingIsA500(t *testing.T) {
	t.Parallel()

	s, b, row := seeded(t, uploaded)
	for key := range b.objects {
		delete(b.objects, key)
	}

	if rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, fileAt(row.TeamID, row.ID, "scripts/fill.py"), nothing); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- list ---

func TestListAsksForOneMoreThanThePage(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=10", nothing)

	if len(s.listArgs) != 1 || s.listArgs[0].PageSize != 11 || s.listArgs[0].TeamID != teamID || s.listArgs[0].UseCursor {
		t.Errorf("ListSkillsByTeam got %+v, want team %s, 11 rows, no cursor", s.listArgs, teamID)
	}
}

// A full page carries a cursor, and handing it back continues after the last
// row shown — every skill once, none twice.
func TestTheNextCursorWalksEverySkillOnce(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	var rows []db.Skill
	for i, name := range []string{"a", "b", "c"} {
		row := aSkill(teamID, name)
		row.CreatedAt = time.Date(2026, 9, 1, 0, 0, i, 0, time.UTC)
		rows = append(rows, row)
	}
	s := newRouteStore(rows...)
	b := newBucket(s.txStore)

	var seen []string
	next := ""
	for range 4 {
		rec := call(t, s, b, &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=1"+next, nothing)
		var page api.Page[summary]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body: %v (%s)", err, rec.Body)
		}
		for _, item := range page.Items {
			seen = append(seen, item.Name)
		}
		if page.NextCursor == nil {
			break
		}
		next = "&cursor=" + *page.NextCursor
	}

	if want := []string{"c", "b", "a"}; !slices.Equal(seen, want) {
		t.Errorf("paged %v, want %v newest first", seen, want)
	}
}

// A list shows at a glance which skills are the team's own.
func TestAListItemCarriesItsSource(t *testing.T) {
	t.Parallel()

	gh := Origin{Source: "github", Ref: text("github:anthropics/skills/pdf@main"), SHA: text("0123abc")}
	s, b, row := seeded(t, gh)

	rec := call(t, s, b, &fakeAuthz{}, memberOf(row.TeamID), http.MethodGet, collection(row.TeamID), nothing)
	var page api.Page[summary]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("page: %v (%s)", err, rec.Body)
	}
	if page.Items[0].Source != "github" {
		t.Errorf("source = %q, want github", page.Items[0].Source)
	}
}

// A limit that is not a number is the caller's mistake, answered before the
// list runs.
func TestABadLimitIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?limit=many", nothing)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if s.touched() {
		t.Error("a bad limit reached the store")
	}
}

// A mangled cursor restarting from the top would turn a client's paging loop
// into an infinite one.
func TestAMangledCursorIsABadRequest(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID)+"?cursor=not-a-cursor", nothing)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if s.touched() {
		t.Error("a mangled cursor reached the store")
	}
}

// An empty team lists as [] rather than null: "we looked, there are none".
func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := newRouteStore()
	rec := call(t, s, newBucket(s.txStore), &fakeAuthz{}, memberOf(teamID), http.MethodGet, collection(teamID), nothing)

	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty list = %s, want items to be []", rec.Body)
	}
}

// --- failures of ours ---

// A failure of ours is a 500 that says nothing: the detail goes to the log,
// and the database's own words never reach the caller.
func TestAFailureOfOursIsA500ThatLeaksNothing(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method  string
		path    func(db.Skill) string
		body    func(*testing.T) sent
		breakIt func(*routeStore, *bucket)
	}{
		"list":            {http.MethodGet, func(r db.Skill) string { return collection(r.TeamID) }, nil, func(s *routeStore, _ *bucket) { s.readErr = errAny }},
		"get":             {http.MethodGet, func(r db.Skill) string { return one(r.TeamID, r.ID) }, nil, func(s *routeStore, _ *bucket) { s.readErr = errAny }},
		"get's files":     {http.MethodGet, func(r db.Skill) string { return one(r.TeamID, r.ID) }, nil, func(s *routeStore, _ *bucket) { s.filesErr = errAny }},
		"create":          {http.MethodPost, func(r db.Skill) string { return collection(r.TeamID) }, func(t *testing.T) sent { return skillNamed(t, "docx") }, func(s *routeStore, _ *bucket) { s.failAt = "CreateSkill" }},
		"create's bucket": {http.MethodPost, func(r db.Skill) string { return collection(r.TeamID) }, func(t *testing.T) sent { return skillNamed(t, "docx") }, func(_ *routeStore, b *bucket) { b.failOn = len(b.objects) + 1 }},
		"replace":         {http.MethodPut, func(r db.Skill) string { return one(r.TeamID, r.ID) }, func(t *testing.T) sent { return skillNamed(t, "pdf") }, func(s *routeStore, _ *bucket) { s.failAt = "UpdateSkill" }},
		"delete":          {http.MethodDelete, func(r db.Skill) string { return one(r.TeamID, r.ID) }, nil, func(s *routeStore, _ *bucket) { s.deleteErr = errAny }},
		"file":            {http.MethodGet, func(r db.Skill) string { return fileAt(r.TeamID, r.ID, "scripts/fill.py") }, nil, func(s *routeStore, _ *bucket) { s.filesErr = errAny }},
		"file's object":   {http.MethodGet, func(r db.Skill) string { return fileAt(r.TeamID, r.ID, "scripts/fill.py") }, nil, func(_ *routeStore, b *bucket) { b.getErr = errAny }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, b, row := seeded(t, uploaded)
			tc.breakIt(s, b)
			in := nothing
			if tc.body != nil {
				in = tc.body(t)
			}

			rec := call(t, s, b, &fakeAuthz{allowed: true}, memberOf(row.TeamID), tc.method, tc.path(row), in)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 (%s)", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "connection reset") || strings.Contains(rec.Body.String(), "failed") {
				t.Errorf("the reply leaked the failure: %s", rec.Body)
			}
		})
	}
}

func skillEntry(path, data string) skill.Entry {
	return skill.Entry{Path: path, Data: []byte(data)}
}
