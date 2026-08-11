package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore implements the same keyset semantics as the SQL: it sorts, applies
// the cursor, then truncates to PageSize. A fake that ignored the cursor would
// let a broken cursor round-trip pass.
type fakeStore struct {
	teams     []db.Team
	members   []db.ListTeamMembersRow
	userTeams []db.ListUserTeamsRow
	err       error

	created      []string
	added        []db.AddTeamMemberParams
	removed      []db.RemoveTeamMemberParams
	teamArgs     []db.ListTeamsParams
	memberArgs   []db.ListTeamMembersParams
	listedUser   int
	getTeamCalls int
}

func (f *fakeStore) CreateTeam(_ context.Context, name string) (db.Team, error) {
	f.created = append(f.created, name)
	if f.err != nil {
		return db.Team{}, f.err
	}
	return db.Team{ID: uuid.New(), Name: name}, nil
}

func (f *fakeStore) GetTeam(_ context.Context, id uuid.UUID) (db.Team, error) {
	f.getTeamCalls++
	if f.err != nil {
		return db.Team{}, f.err
	}
	for _, t := range f.teams {
		if t.ID == id {
			return t, nil
		}
	}
	return db.Team{}, pgx.ErrNoRows
}

func (f *fakeStore) ListTeams(_ context.Context, arg db.ListTeamsParams) ([]db.Team, error) {
	f.teamArgs = append(f.teamArgs, arg)
	if f.err != nil {
		return nil, f.err
	}

	rows := slices.Clone(f.teams)
	// created_at DESC, id DESC
	slices.SortFunc(rows, func(a, b db.Team) int {
		return -compareKey(a.CreatedAt.String(), a.ID, b.CreatedAt.String(), b.ID)
	})
	if arg.UseCursor {
		rows = slices.DeleteFunc(rows, func(t db.Team) bool {
			return compareKey(t.CreatedAt.String(), t.ID, arg.AfterCreatedAt.String(), arg.AfterID) >= 0
		})
	}
	return truncate(rows, arg.PageSize), nil
}

func (f *fakeStore) ListTeamMembers(_ context.Context, arg db.ListTeamMembersParams) ([]db.ListTeamMembersRow, error) {
	f.memberArgs = append(f.memberArgs, arg)
	if f.err != nil {
		return nil, f.err
	}

	rows := slices.Clone(f.members)
	// subject ASC, id ASC
	slices.SortFunc(rows, func(a, b db.ListTeamMembersRow) int {
		return compareKey(a.Subject, a.ID, b.Subject, b.ID)
	})
	if arg.UseCursor {
		rows = slices.DeleteFunc(rows, func(m db.ListTeamMembersRow) bool {
			return compareKey(m.Subject, m.ID, arg.AfterSubject, arg.AfterID) <= 0
		})
	}
	return truncate(rows, arg.PageSize), nil
}

func (f *fakeStore) ListUserTeams(context.Context, uuid.UUID) ([]db.ListUserTeamsRow, error) {
	f.listedUser++
	return f.userTeams, f.err
}

func (f *fakeStore) AddTeamMember(_ context.Context, arg db.AddTeamMemberParams) (db.TeamMember, error) {
	f.added = append(f.added, arg)
	if f.err != nil {
		return db.TeamMember{}, f.err
	}
	return db.TeamMember{TeamID: arg.TeamID, UserID: arg.UserID, Role: arg.Role}, nil
}

func (f *fakeStore) RemoveTeamMember(_ context.Context, arg db.RemoveTeamMemberParams) error {
	f.removed = append(f.removed, arg)
	return f.err
}

// compareKey orders a (text, uuid) pair the way Postgres orders a row
// constructor: the first column decides unless it ties.
func compareKey(a string, aID uuid.UUID, b string, bID uuid.UUID) int {
	if c := strings.Compare(a, b); c != 0 {
		return c
	}
	return strings.Compare(aID.String(), bID.String())
}

func truncate[T any](rows []T, size int32) []T {
	if int(size) < len(rows) {
		return rows[:size]
	}
	return rows
}

// fakeAuthz answers both questions this package asks.
type fakeAuthz struct {
	super   bool
	allowed bool
	err     error
	asked   []authz.Action
}

func (f *fakeAuthz) IsSuperAdmin(*auth.User) bool { return f.super }

func (f *fakeAuthz) Can(_ context.Context, _ *auth.User, a authz.Action, _ authz.Resource) (bool, error) {
	f.asked = append(f.asked, a)
	return f.allowed, f.err
}

// call drives one request through the registered routes with the user already
// on the context, exactly as the middleware chain leaves it. Driving the mux
// rather than the handler puts the pattern under test too.
func call(t *testing.T, s Store, a Authorizer, u *auth.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	New(discardLogger(), s, a).Register(mux)

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, reader)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func memberOf(teamID uuid.UUID, role string) *auth.User {
	return &auth.User{
		ID:      uuid.New(),
		Issuer:  "dev",
		Subject: "someone",
		Teams:   []auth.Membership{{TeamID: teamID, Name: "platform", Role: role}},
	}
}

func outsider() *auth.User {
	return &auth.User{ID: uuid.New(), Issuer: "dev", Subject: "outsider"}
}

func teamsPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[team] {
	t.Helper()

	var got api.Page[team]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of teams: %v (%s)", err, rec.Body)
	}
	return got
}

func membersPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[member] {
	t.Helper()

	var got api.Page[member]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of members: %v (%s)", err, rec.Body)
	}
	return got
}

// pgErr builds the error Postgres returns for a rejected constraint.
func pgErr(code, constraint string) error {
	return &pgconn.PgError{Code: code, ConstraintName: constraint}
}

// --- POST /teams ---------------------------------------------------------

func TestCreateMakesATeamForASuperAdmin(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if len(s.created) != 1 || s.created[0] != "platform" {
		t.Errorf("created = %q, want [platform]", s.created)
	}

	var got team
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a team: %v (%s)", err, rec.Body)
	}
	if got.Name != "platform" {
		t.Errorf("Name = %q, want platform", got.Name)
	}
	if got.ID == uuid.Nil {
		t.Error("the reply carries no id, so a client cannot address the team it just made")
	}
	if got.Role != nil {
		t.Errorf("Role = %q on a freshly created team, which has no members", *got.Role)
	}
}

// Creation is platform-scoped. A team admin is not a platform admin, so being
// admin somewhere must not be enough — and Can must not be consulted, because
// there is no team to scope it to.
func TestCreateIsRefusedToEveryoneElse(t *testing.T) {
	t.Parallel()

	for name, u := range map[string]*auth.User{
		"an outsider":        outsider(),
		"an admin of a team": memberOf(uuid.New(), "admin"),
		"a developer of one": memberOf(uuid.New(), "developer"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			a := &fakeAuthz{allowed: true}
			rec := call(t, s, a, u, http.MethodPost, "/teams", `{"name":"platform"}`)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if len(s.created) != 0 {
				t.Errorf("the store was written to anyway: %q", s.created)
			}
			if len(a.asked) != 0 {
				t.Errorf("Can was consulted for a platform-scoped route: %v", a.asked)
			}
		})
	}
}

// The authorisation check comes before the body is read, so a forbidden caller
// cannot tell a valid body from an invalid one.
func TestCreateChecksAuthorisationBeforeTheBody(t *testing.T) {
	t.Parallel()

	valid := call(t, &fakeStore{}, &fakeAuthz{}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)
	garbage := call(t, &fakeStore{}, &fakeAuthz{}, outsider(), http.MethodPost, "/teams", `garbage`)

	if valid.Code != garbage.Code {
		t.Errorf("a valid body gives %d and garbage gives %d — the difference tells an outsider what the schema is",
			valid.Code, garbage.Code)
	}
}

func TestCreateRejectsABadName(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"missing":     `{}`,
		"empty":       `{"name":""}`,
		"only spaces": `{"name":"   "}`,
		"only a tab":  `{"name":"\t"}`,
		"too long":    `{"name":"` + strings.Repeat("a", maxNameBytes+1) + `"}`,
		"wrong type":  `{"name":42}`,
		"unknown key": `{"name":"a","owner":"b"}`,
		"malformed":   `{"name":`,
		"two objects": `{"name":"a"}{"name":"b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(s.created) != 0 {
				t.Errorf("reached the store with %q", s.created)
			}
		})
	}
}

// A name is stored trimmed, or " platform" and "platform" become two teams
// that look identical in every listing.
func TestCreateTrimsTheName(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"  platform\n"}`)

	if len(s.created) != 1 || s.created[0] != "platform" {
		t.Errorf("created = %q, want [platform]", s.created)
	}
}

// A name exactly at the limit is legal. An off-by-one here rejects a name the
// documented limit says is fine.
func TestCreateAcceptsANameAtTheLimit(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("a", maxNameBytes)
	s := &fakeStore{}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"`+name+`"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if len(s.created) != 1 || s.created[0] != name {
		t.Error("a name at the limit was not stored")
	}
}

func TestCreateReportsAStoreFailureAsInternal(t *testing.T) {
	t.Parallel()

	s := &fakeStore{err: errors.New("connection refused")}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the reply leaked the database error: %s", rec.Body)
	}
}

// --- GET /teams ----------------------------------------------------------

func TestListGivesASuperAdminEveryTeam(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: []db.Team{
		{ID: uuid.New(), Name: "platform", CreatedAt: time.Unix(200, 0)},
		{ID: uuid.New(), Name: "payments", CreatedAt: time.Unix(100, 0)},
	}}

	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if s.listedUser != 0 {
		t.Error("a super admin was scoped to their own memberships")
	}

	got := teamsPage(t, rec)
	if len(got.Items) != 2 {
		t.Fatalf("got %d teams, want 2", len(got.Items))
	}
	for _, tm := range got.Items {
		if tm.Role != nil {
			t.Errorf("team %q carries role %q, but a super admin is not a member of it", tm.Name, *tm.Role)
		}
	}
}

func TestListGivesEveryoneElseOnlyTheirOwnTeams(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{
		teams:     []db.Team{{ID: uuid.New(), Name: "payments"}}, // must not appear
		userTeams: []db.ListUserTeamsRow{{ID: id, Name: "platform", Role: "developer"}},
	}

	rec := call(t, s, &fakeAuthz{}, memberOf(id, "developer"), http.MethodGet, "/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(s.teamArgs) != 0 {
		t.Error("a non-super-admin was served the full team list")
	}

	got := teamsPage(t, rec)
	if len(got.Items) != 1 || got.Items[0].ID != id {
		t.Fatalf("got %+v, want only the caller's team", got.Items)
	}
	if got.Items[0].Role == nil || *got.Items[0].Role != "developer" {
		t.Errorf("Role = %v, want developer — a member's own listing must carry it", got.Items[0].Role)
	}
	if got.NextCursor != nil {
		t.Errorf("a membership listing carries a cursor %q, but it is one page by construction", *got.NextCursor)
	}
}

// Each entry keeps its own role. Taking the address of a range variable is the
// classic way to give every entry the last one.
func TestListGivesEachTeamItsOwnRole(t *testing.T) {
	t.Parallel()

	s := &fakeStore{userTeams: []db.ListUserTeamsRow{
		{ID: uuid.New(), Name: "platform", Role: "admin"},
		{ID: uuid.New(), Name: "payments", Role: "developer"},
	}}

	got := teamsPage(t, call(t, s, &fakeAuthz{}, outsider(), http.MethodGet, "/teams", ""))

	if len(got.Items) != 2 {
		t.Fatalf("got %d teams, want 2", len(got.Items))
	}
	if got.Items[0].Role == nil || got.Items[1].Role == nil {
		t.Fatal("a role is missing")
	}
	if *got.Items[0].Role != "admin" || *got.Items[1].Role != "developer" {
		t.Errorf("roles = %q, %q; want admin, developer", *got.Items[0].Role, *got.Items[1].Role)
	}
}

// No teams is an answer. A null body makes a client that iterates the result
// crash on something that is not an error.
func TestListReturnsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()

	for name, a := range map[string]Authorizer{
		"super admin": &fakeAuthz{super: true},
		"member":      &fakeAuthz{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := call(t, &fakeStore{}, a, outsider(), http.MethodGet, "/teams", "")
			if body := strings.TrimSpace(rec.Body.String()); body != `{"items":[]}` {
				t.Errorf("body = %s, want {\"items\":[]}", body)
			}
		})
	}
}

// The end of the collection is signalled by the cursor being absent, so a last
// page must not carry one.
func TestListOmitsTheCursorOnTheLastPage(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: []db.Team{{ID: uuid.New(), Name: "platform", CreatedAt: time.Unix(100, 0)}}}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if strings.Contains(rec.Body.String(), "next_cursor") {
		t.Errorf("a single-page result carries a cursor: %s", rec.Body)
	}
}

// The whole point of the sentinel row: ask for limit+1, and the extra one says
// another page exists.
func TestListAsksForOneRowMoreThanTheLimit(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: makeTeams(5)}
	call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams?limit=2", "")

	if len(s.teamArgs) != 1 {
		t.Fatalf("queried %d times, want 1", len(s.teamArgs))
	}
	if s.teamArgs[0].PageSize != 3 {
		t.Errorf("PageSize = %d, want 3 — without the extra row the cursor cannot be decided",
			s.teamArgs[0].PageSize)
	}
}

// Walking every page must yield each team exactly once. This is the test that
// catches an off-by-one in the cursor or a comparison pointing the wrong way.
func TestListPagesThroughEveryTeamExactlyOnce(t *testing.T) {
	t.Parallel()

	all := makeTeams(7)
	s := &fakeStore{teams: all}

	seen := map[uuid.UUID]int{}
	path := "/teams?limit=2"

	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}

		rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}

		page := teamsPage(t, rec)
		if len(page.Items) > 2 {
			t.Fatalf("page holds %d teams, want at most 2", len(page.Items))
		}
		for _, tm := range page.Items {
			seen[tm.ID]++
		}
		if page.NextCursor == nil {
			break
		}
		path = "/teams?limit=2&cursor=" + *page.NextCursor
	}

	if len(seen) != len(all) {
		t.Errorf("saw %d distinct teams, want %d", len(seen), len(all))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("team %s appeared %d times", id, n)
		}
	}
}

// A cursor must survive a tie on the ordering column, or the paging loop either
// repeats or skips the tied rows forever.
func TestListPagesThroughTeamsSharingATimestamp(t *testing.T) {
	t.Parallel()

	same := time.Unix(500, 0)
	all := []db.Team{
		{ID: uuid.New(), Name: "a", CreatedAt: same},
		{ID: uuid.New(), Name: "b", CreatedAt: same},
		{ID: uuid.New(), Name: "c", CreatedAt: same},
	}
	s := &fakeStore{teams: all}

	seen := map[uuid.UUID]int{}
	path := "/teams?limit=1"

	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate on tied timestamps")
		}

		page := teamsPage(t, call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, path, ""))
		for _, tm := range page.Items {
			seen[tm.ID]++
		}
		if page.NextCursor == nil {
			break
		}
		path = "/teams?limit=1&cursor=" + *page.NextCursor
	}

	if len(seen) != len(all) {
		t.Errorf("saw %d of %d teams that share a timestamp", len(seen), len(all))
	}
}

func TestListRejectsABadLimit(t *testing.T) {
	t.Parallel()

	// "?limit=" with no value is not in this list: an empty value reads the
	// same as omitting the parameter, and defaulting is kinder than a 400.
	for _, raw := range []string{"0", "-1", "abc", "1.5", "999999999999999999999"} {
		s := &fakeStore{}
		rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams?limit="+raw, "")

		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%q gave %d, want 400", raw, rec.Code)
		}
		if len(s.teamArgs) != 0 {
			t.Errorf("limit=%q reached the store", raw)
		}
	}
}

// A limit above the cap is clamped rather than refused: a client guessing high
// should get the maximum, not an error.
func TestListClampsAnOversizedLimit(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams?limit=100000", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(s.teamArgs) != 1 || s.teamArgs[0].PageSize != api.MaxLimit+1 {
		t.Errorf("PageSize = %d, want %d", s.teamArgs[0].PageSize, api.MaxLimit+1)
	}
}

// A cursor the client mangled is a client error. Starting from the top instead
// would turn their paging loop into an infinite one.
func TestListRejectsAMangledCursor(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"not base64":     "!!!!",
		"not json":       "aGVsbG8",
		"json but wrong": "WzEsMiwzXQ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams?cursor="+raw, "")

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(s.teamArgs) != 0 {
				t.Error("a bad cursor reached the store")
			}
		})
	}
}

// Without a cursor the query must not filter, or the first page is empty.
func TestListDoesNotUseACursorOnTheFirstPage(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: makeTeams(2)}
	call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if len(s.teamArgs) != 1 {
		t.Fatalf("queried %d times, want 1", len(s.teamArgs))
	}
	if s.teamArgs[0].UseCursor {
		t.Error("the first page was filtered by a cursor nobody sent")
	}
}

func TestListReportsAStoreFailureAsInternal(t *testing.T) {
	t.Parallel()

	for name, a := range map[string]Authorizer{
		"super admin": &fakeAuthz{super: true},
		"member":      &fakeAuthz{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{err: errors.New("connection refused")}
			rec := call(t, s, a, outsider(), http.MethodGet, "/teams", "")

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Errorf("the reply leaked the database error: %s", rec.Body)
			}
		})
	}
}

// --- GET /teams/{id} -----------------------------------------------------

func TestGetReturnsATeamToItsMember(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}

	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got team
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a team: %v (%s)", err, rec.Body)
	}
	if got.Role == nil || *got.Role != "admin" {
		t.Errorf("Role = %v, want admin", got.Role)
	}
}

// A super admin can read a team they are not in, and the reply says so by
// leaving the role out rather than inventing one.
func TestGetReturnsATeamToASuperAdminWithoutARole(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}

	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+id.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not an object: %v", err)
	}
	if _, present := got["role"]; present {
		t.Errorf("role is present for a non-member: %s", rec.Body)
	}
}

// A team the caller may not see and a team that does not exist answer
// identically. A 403 for the first would confirm the id is real, which lets an
// outsider enumerate teams.
func TestGetHidesOtherTeamsBehindTheSame404(t *testing.T) {
	t.Parallel()

	real := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: real, Name: "payments"}}}

	other := call(t, s, &fakeAuthz{}, memberOf(uuid.New(), "admin"), http.MethodGet, "/teams/"+real.String(), "")
	missing := call(t, s, &fakeAuthz{}, memberOf(uuid.New(), "admin"), http.MethodGet, "/teams/"+uuid.New().String(), "")

	if other.Code != http.StatusNotFound {
		t.Errorf("another team's id = %d, want 404", other.Code)
	}
	if missing.Code != http.StatusNotFound {
		t.Errorf("a nonexistent id = %d, want 404", missing.Code)
	}
	if other.Body.String() != missing.Body.String() {
		t.Errorf("the two 404s differ: %q versus %q — the difference is the oracle",
			other.Body, missing.Body)
	}
}

// The membership check runs before the query, so a stranger's probe never
// reaches the database.
func TestGetDoesNotQueryForANonMember(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	call(t, s, &fakeAuthz{}, outsider(), http.MethodGet, "/teams/"+uuid.New().String(), "")

	if s.getTeamCalls != 0 {
		t.Errorf("the store was queried %d times for a caller with no access", s.getTeamCalls)
	}
}

func TestGetIsNotFoundForAMissingTeam(t *testing.T) {
	t.Parallel()

	rec := call(t, &fakeStore{}, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+uuid.New().String(), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGetRejectsAMalformedID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"abc", "123", "not-a-uuid", "%20"} {
		s := &fakeStore{}
		rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+id, "")

		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q = %d, want 400", id, rec.Code)
		}
		if s.getTeamCalls != 0 {
			t.Errorf("id %q reached the store", id)
		}
	}
}

// A read failure is unknown, not absent. Reporting it as 404 tells a client
// the team is gone when it is only unreachable.
func TestGetReportsAReadFailureAsInternal(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{err: errors.New("connection refused")}
	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the reply leaked the database error: %s", rec.Body)
	}
}

// --- GET /teams/{id}/members ---------------------------------------------

func TestListMembersReturnsTheTeamsMembers(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	userID := uuid.New()
	email := "alice@example.com"
	s := &fakeStore{
		teams:   []db.Team{{ID: id, Name: "platform"}},
		members: []db.ListTeamMembersRow{{ID: userID, Subject: "alice", Email: &email, Role: "admin"}},
	}

	rec := call(t, s, &fakeAuthz{}, memberOf(id, "developer"), http.MethodGet, "/teams/"+id.String()+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := membersPage(t, rec)
	if len(got.Items) != 1 {
		t.Fatalf("got %d members, want 1", len(got.Items))
	}
	if got.Items[0].UserID != userID || got.Items[0].Subject != "alice" || got.Items[0].Role != "admin" {
		t.Errorf("member = %+v", got.Items[0])
	}
	if got.Items[0].Email == nil || *got.Items[0].Email != email {
		t.Errorf("Email = %v, want %q", got.Items[0].Email, email)
	}
}

// A user whose IdP released no email is normal, and the key is dropped rather
// than sent as an empty string that looks like an address.
func TestListMembersOmitsAMissingEmail(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{
		teams:   []db.Team{{ID: id, Name: "platform"}},
		members: []db.ListTeamMembersRow{{ID: uuid.New(), Subject: "bob", Role: "developer"}},
	}

	rec := call(t, s, &fakeAuthz{}, memberOf(id, "developer"), http.MethodGet, "/teams/"+id.String()+"/members", "")

	if strings.Contains(rec.Body.String(), "email") {
		t.Errorf("a member with no email still carries the key: %s", rec.Body)
	}
}

// Listing members is a read scoped by visibility, not by an action, so Can is
// not consulted — and a caller who cannot see the team gets the same 404 as
// one asking about a team that does not exist.
func TestListMembersIsHiddenFromNonMembers(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	a := &fakeAuthz{allowed: true}

	rec := call(t, s, a, outsider(), http.MethodGet, "/teams/"+id.String()+"/members", "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if len(s.memberArgs) != 0 {
		t.Error("the member query ran for a caller who cannot see the team")
	}
	if len(a.asked) != 0 {
		t.Errorf("Can was consulted for a read: %v", a.asked)
	}
}

func TestListMembersIsNotFoundForAMissingTeam(t *testing.T) {
	t.Parallel()

	rec := call(t, &fakeStore{}, &fakeAuthz{super: true}, outsider(),
		http.MethodGet, "/teams/"+uuid.New().String()+"/members", "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Members share a subject across issuers, so the cursor has to carry the id
// too. Without it this loop never terminates or drops a row.
func TestListMembersPagesThroughSharedSubjects(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	all := []db.ListTeamMembersRow{
		{ID: uuid.New(), Subject: "bob", Role: "admin"},
		{ID: uuid.New(), Subject: "bob", Role: "developer"},
		{ID: uuid.New(), Subject: "carol", Role: "developer"},
	}
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}, members: all}

	seen := map[uuid.UUID]int{}
	path := fmt.Sprintf("/teams/%s/members?limit=1", id)

	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate on a shared subject")
		}

		page := membersPage(t, call(t, s, &fakeAuthz{}, memberOf(id, "developer"), http.MethodGet, path, ""))
		for _, m := range page.Items {
			seen[m.UserID]++
		}
		if page.NextCursor == nil {
			break
		}
		path = fmt.Sprintf("/teams/%s/members?limit=1&cursor=%s", id, *page.NextCursor)
	}

	if len(seen) != len(all) {
		t.Errorf("saw %d of %d members", len(seen), len(all))
	}
	for userID, n := range seen {
		if n != 1 {
			t.Errorf("member %s appeared %d times", userID, n)
		}
	}
}

func TestListMembersScopesTheQueryToTheTeam(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	call(t, s, &fakeAuthz{}, memberOf(id, "developer"), http.MethodGet, "/teams/"+id.String()+"/members", "")

	if len(s.memberArgs) != 1 {
		t.Fatalf("queried %d times, want 1", len(s.memberArgs))
	}
	if s.memberArgs[0].TeamID != id {
		t.Errorf("queried team %s, want %s", s.memberArgs[0].TeamID, id)
	}
}

func TestListMembersRejectsABadLimit(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"),
		http.MethodGet, "/teams/"+id.String()+"/members?limit=0", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(s.memberArgs) != 0 {
		t.Error("a bad limit reached the store")
	}
}

func TestListMembersRejectsAMangledCursor(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"),
		http.MethodGet, "/teams/"+id.String()+"/members?cursor=!!!!", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(s.memberArgs) != 0 {
		t.Error("a bad cursor reached the store")
	}
}

func TestListMembersAsksForOneRowMoreThanTheLimit(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	call(t, s, &fakeAuthz{}, memberOf(id, "admin"),
		http.MethodGet, "/teams/"+id.String()+"/members?limit=3", "")

	if len(s.memberArgs) != 1 {
		t.Fatalf("queried %d times, want 1", len(s.memberArgs))
	}
	if s.memberArgs[0].PageSize != 4 {
		t.Errorf("PageSize = %d, want 4", s.memberArgs[0].PageSize)
	}
	if s.memberArgs[0].UseCursor {
		t.Error("the first page was filtered by a cursor nobody sent")
	}
}

func TestListMembersReportsAStoreFailureAsInternal(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}, err: errors.New("connection refused")}
	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"), http.MethodGet, "/teams/"+id.String()+"/members", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the reply leaked the database error: %s", rec.Body)
	}
}

// --- POST /teams/{id}/members --------------------------------------------

func TestAddMemberAddsTheUser(t *testing.T) {
	t.Parallel()

	id, userID := uuid.New(), uuid.New()
	s := &fakeStore{}
	a := &fakeAuthz{allowed: true}

	body := fmt.Sprintf(`{"user_id":%q,"role":"developer"}`, userID)
	rec := call(t, s, a, memberOf(id, "admin"), http.MethodPost, "/teams/"+id.String()+"/members", body)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if len(s.added) != 1 {
		t.Fatalf("added %d members, want 1", len(s.added))
	}
	if s.added[0].TeamID != id || s.added[0].UserID != userID || s.added[0].Role != "developer" {
		t.Errorf("added %+v", s.added[0])
	}
	if !slices.Contains(a.asked, authz.ActionMemberWrite) {
		t.Errorf("asked for %v, want member:write", a.asked)
	}
}

func TestAddMemberIsRefusedWithoutTheAction(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{}
	body := fmt.Sprintf(`{"user_id":%q,"role":"developer"}`, uuid.New())

	rec := call(t, s, &fakeAuthz{allowed: false}, memberOf(id, "developer"),
		http.MethodPost, "/teams/"+id.String()+"/members", body)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if len(s.added) != 0 {
		t.Error("the store was written to anyway")
	}
}

// A failed permission read is unknown, not denied. Collapsing it to 403 makes
// a database blip look like a permissions problem.
func TestAddMemberReportsAnAuthorisationFailureAsInternal(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	a := &fakeAuthz{err: errors.New("roles table is gone")}
	body := fmt.Sprintf(`{"user_id":%q,"role":"developer"}`, uuid.New())

	rec := call(t, &fakeStore{}, a, memberOf(id, "admin"), http.MethodPost, "/teams/"+id.String()+"/members", body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "roles table") {
		t.Errorf("the reply leaked the error: %s", rec.Body)
	}
}

// The constraint the database rejected names the client's mistake, so each one
// gets its own status rather than a blanket 500.
func TestAddMemberMapsConstraintsToStatuses(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"already a member": {pgErr("23505", "team_members_pkey"), http.StatusConflict},
		"unknown team":     {pgErr("23503", "team_members_team_id_fkey"), http.StatusNotFound},
		"unknown user":     {pgErr("23503", "team_members_user_id_fkey"), http.StatusBadRequest},
		"unknown role":     {pgErr("23503", "team_members_role_fkey"), http.StatusBadRequest},
		"anything else":    {pgErr("08006", ""), http.StatusInternalServerError},
		"not a pg error":   {errors.New("connection refused"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			id := uuid.New()
			s := &fakeStore{err: tc.err}
			body := fmt.Sprintf(`{"user_id":%q,"role":"developer"}`, uuid.New())

			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(id, "admin"),
				http.MethodPost, "/teams/"+id.String()+"/members", body)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestAddMemberRejectsABadBody(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"empty":            ``,
		"malformed":        `{"user_id":`,
		"user id not uuid": `{"user_id":"abc","role":"developer"}`,
		"user id a number": `{"user_id":42,"role":"developer"}`,
		"unknown key":      fmt.Sprintf(`{"user_id":%q,"role":"developer","admin":true}`, uuid.New()),
		"two objects":      `{"user_id":"x"}{"user_id":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			id := uuid.New()
			s := &fakeStore{}
			rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(id, "admin"),
				http.MethodPost, "/teams/"+id.String()+"/members", body)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(s.added) != 0 {
				t.Error("a bad body reached the store")
			}
		})
	}
}

// The authorisation check runs before the body is read, so a caller without
// the action cannot probe the request schema.
func TestAddMemberChecksAuthorisationBeforeTheBody(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	valid := fmt.Sprintf(`{"user_id":%q,"role":"developer"}`, uuid.New())

	ok := call(t, &fakeStore{}, &fakeAuthz{}, memberOf(id, "developer"),
		http.MethodPost, "/teams/"+id.String()+"/members", valid)
	garbage := call(t, &fakeStore{}, &fakeAuthz{}, memberOf(id, "developer"),
		http.MethodPost, "/teams/"+id.String()+"/members", "garbage")

	if ok.Code != garbage.Code {
		t.Errorf("a valid body gives %d and garbage gives %d", ok.Code, garbage.Code)
	}
}

// --- DELETE /teams/{id}/members/{userID} ---------------------------------

func TestRemoveMemberRemovesTheUser(t *testing.T) {
	t.Parallel()

	id, userID := uuid.New(), uuid.New()
	s := &fakeStore{}
	a := &fakeAuthz{allowed: true}

	path := fmt.Sprintf("/teams/%s/members/%s", id, userID)
	rec := call(t, s, a, memberOf(id, "admin"), http.MethodDelete, path, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if len(s.removed) != 1 || s.removed[0].TeamID != id || s.removed[0].UserID != userID {
		t.Errorf("removed %+v", s.removed)
	}
	if !slices.Contains(a.asked, authz.ActionMemberWrite) {
		t.Errorf("asked for %v, want member:write", a.asked)
	}
}

func TestRemoveMemberIsRefusedWithoutTheAction(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{}
	path := fmt.Sprintf("/teams/%s/members/%s", id, uuid.New())

	rec := call(t, s, &fakeAuthz{allowed: false}, memberOf(id, "developer"), http.MethodDelete, path, "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if len(s.removed) != 0 {
		t.Error("the store was written to anyway")
	}
}

func TestRemoveMemberRejectsAMalformedUserID(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	for _, userID := range []string{"abc", "123", "not-a-uuid"} {
		s := &fakeStore{}
		path := fmt.Sprintf("/teams/%s/members/%s", id, userID)
		rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(id, "admin"), http.MethodDelete, path, "")

		if rec.Code != http.StatusBadRequest {
			t.Errorf("user id %q = %d, want 400", userID, rec.Code)
		}
		if len(s.removed) != 0 {
			t.Errorf("user id %q reached the store", userID)
		}
	}
}

func TestRemoveMemberReportsAStoreFailureAsInternal(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{err: errors.New("connection refused")}
	path := fmt.Sprintf("/teams/%s/members/%s", id, uuid.New())

	rec := call(t, s, &fakeAuthz{allowed: true}, memberOf(id, "admin"), http.MethodDelete, path, "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the reply leaked the database error: %s", rec.Body)
	}
}

// --- the shape of every route --------------------------------------------

// Without the middleware there is no user on the context. That is a wiring
// bug — the route is on the wrong mux — so it must fail loudly rather than
// serve as an anonymous caller.
func TestEveryRouteRefusesARequestWithNoUser(t *testing.T) {
	t.Parallel()

	id, userID := uuid.New(), uuid.New()
	for name, tc := range map[string]struct{ method, path, body string }{
		"create":  {http.MethodPost, "/teams", `{"name":"platform"}`},
		"list":    {http.MethodGet, "/teams", ""},
		"get":     {http.MethodGet, "/teams/" + id.String(), ""},
		"members": {http.MethodGet, "/teams/" + id.String() + "/members", ""},
		"add":     {http.MethodPost, "/teams/" + id.String() + "/members", `{"user_id":"` + userID.String() + `","role":"admin"}`},
		"remove":  {http.MethodDelete, "/teams/" + id.String() + "/members/" + userID.String(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, &fakeAuthz{super: true, allowed: true}, nil, tc.method, tc.path, tc.body)

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if s.getTeamCalls != 0 || len(s.teamArgs) != 0 || len(s.memberArgs) != 0 ||
				s.listedUser != 0 || len(s.created) != 0 || len(s.added) != 0 || len(s.removed) != 0 {
				t.Error("the handler reached the store without a user")
			}
		})
	}
}

// The verbs are part of the contract. A route answering a verb it never
// declared is how a read endpoint quietly becomes a write one.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	id, userID := uuid.New().String(), uuid.New().String()
	for name, tc := range map[string]struct{ method, path string }{
		"PUT on the collection": {http.MethodPut, "/teams"},
		"DELETE on one team":    {http.MethodDelete, "/teams/" + id},
		"POST on one team":      {http.MethodPost, "/teams/" + id},
		"PATCH on one team":     {http.MethodPatch, "/teams/" + id},
		"DELETE on members":     {http.MethodDelete, "/teams/" + id + "/members"},
		"PUT on members":        {http.MethodPut, "/teams/" + id + "/members"},
		"GET on one member":     {http.MethodGet, "/teams/" + id + "/members/" + userID},
		"POST on one member":    {http.MethodPost, "/teams/" + id + "/members/" + userID},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, &fakeAuthz{super: true, allowed: true}, outsider(), tc.method, tc.path, "")

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if s.getTeamCalls != 0 || len(s.created) != 0 || len(s.added) != 0 || len(s.removed) != 0 {
				t.Error("an undeclared method reached the store")
			}
		})
	}
}

// The wire shape is a contract. This fails when a struct grows a field, rather
// than when a client notices the extra key.
func TestOnlyContractedFieldsAreSerialised(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not an object: %v", err)
	}

	want := map[string]bool{"id": true, "name": true, "role": true}
	for key := range got {
		if !want[key] {
			t.Errorf("unexpected field %q in the response: %s", key, rec.Body)
		}
	}
	for key := range want {
		if _, present := got[key]; !present {
			t.Errorf("field %q is missing", key)
		}
	}
}

func TestOnlyContractedMemberFieldsAreSerialised(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	email := "alice@example.com"
	s := &fakeStore{
		teams:   []db.Team{{ID: id, Name: "platform"}},
		members: []db.ListTeamMembersRow{{ID: uuid.New(), Subject: "alice", Email: &email, Role: "admin"}},
	}

	rec := call(t, s, &fakeAuthz{}, memberOf(id, "admin"), http.MethodGet, "/teams/"+id.String()+"/members", "")

	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %d members, want 1", len(got.Items))
	}

	want := map[string]bool{"user_id": true, "subject": true, "email": true, "role": true}
	for key := range got.Items[0] {
		if !want[key] {
			t.Errorf("unexpected field %q: %s", key, rec.Body)
		}
	}
}

// created_at is only available on the ListTeams path. Publishing it would make
// the response shape depend on who is asking.
func TestTimestampsAreNotPublished(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: []db.Team{{ID: uuid.New(), Name: "platform", CreatedAt: time.Unix(100, 0)}}}
	rec := call(t, s, &fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if body := rec.Body.String(); strings.Contains(body, "created_at") || strings.Contains(body, "updated_at") {
		t.Errorf("a timestamp reached the wire: %s", body)
	}
}

func TestEveryResponseIsJSON(t *testing.T) {
	t.Parallel()

	id := uuid.New()

	for name, tc := range map[string]struct{ method, path, body string }{
		"create":  {http.MethodPost, "/teams", `{"name":"platform"}`},
		"list":    {http.MethodGet, "/teams", ""},
		"get":     {http.MethodGet, "/teams/" + id.String(), ""},
		"members": {http.MethodGet, "/teams/" + id.String() + "/members", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Its own store: fakeStore records every call, so sharing one
			// across parallel subtests is a data race.
			s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}
			rec := call(t, s, &fakeAuthz{super: true}, outsider(), tc.method, tc.path, tc.body)
			if ct := rec.Result().Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}

// makeTeams builds n teams with distinct, descending timestamps.
func makeTeams(n int) []db.Team {
	teams := make([]db.Team, n)
	for i := range teams {
		teams[i] = db.Team{
			ID:        uuid.New(),
			Name:      fmt.Sprintf("team-%d", i),
			CreatedAt: time.Unix(int64(1000-i), 0),
		}
	}
	return teams
}
