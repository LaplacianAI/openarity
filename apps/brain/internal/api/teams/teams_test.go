package teams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore records what it was asked and returns what the test set up. Errors
// take precedence so a failure path can be driven without rows.
type fakeStore struct {
	teams     []db.Team
	userTeams []db.ListUserTeamsRow
	err       error

	created      []string
	listedAll    int
	listedUser   int
	gotTeamID    uuid.UUID
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
	f.gotTeamID = id
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

func (f *fakeStore) ListTeams(context.Context) ([]db.Team, error) {
	f.listedAll++
	return f.teams, f.err
}

func (f *fakeStore) ListUserTeams(context.Context, uuid.UUID) ([]db.ListUserTeamsRow, error) {
	f.listedUser++
	return f.userTeams, f.err
}

// fakeAuthz answers the one question this package asks.
type fakeAuthz struct{ super bool }

func (f fakeAuthz) IsSuperAdmin(*auth.User) bool { return f.super }

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

func member(teamID uuid.UUID, role string) *auth.User {
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

func decodeTeams(t *testing.T, rec *httptest.ResponseRecorder) []team {
	t.Helper()

	var got []team
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a JSON array of teams: %v (%s)", err, rec.Body)
	}
	return got
}

// --- POST /teams ---------------------------------------------------------

func TestCreateMakesATeamForASuperAdmin(t *testing.T) {
	t.Parallel()

	s := &fakeStore{}
	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)

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
// admin somewhere must not be enough.
func TestCreateIsRefusedToEveryoneElse(t *testing.T) {
	t.Parallel()

	for name, u := range map[string]*auth.User{
		"an outsider":        outsider(),
		"an admin of a team": member(uuid.New(), "admin"),
		"a developer of one": member(uuid.New(), "developer"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, fakeAuthz{}, u, http.MethodPost, "/teams", `{"name":"platform"}`)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if len(s.created) != 0 {
				t.Errorf("the store was written to anyway: %q", s.created)
			}
		})
	}
}

// The authorisation check comes before the body is read, so a forbidden caller
// cannot tell a valid body from an invalid one.
func TestCreateChecksAuthorisationBeforeTheBody(t *testing.T) {
	t.Parallel()

	valid := call(t, &fakeStore{}, fakeAuthz{}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)
	garbage := call(t, &fakeStore{}, fakeAuthz{}, outsider(), http.MethodPost, "/teams", `garbage`)

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
			rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", body)

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
	call(t, s, fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"  platform\n"}`)

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
	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"`+name+`"}`)

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
	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodPost, "/teams", `{"name":"platform"}`)

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

	a, b := uuid.New(), uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: a, Name: "platform"}, {ID: b, Name: "payments"}}}

	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if s.listedUser != 0 {
		t.Error("a super admin was scoped to their own memberships")
	}

	got := decodeTeams(t, rec)
	if len(got) != 2 {
		t.Fatalf("got %d teams, want 2", len(got))
	}
	for _, tm := range got {
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

	rec := call(t, s, fakeAuthz{}, member(id, "developer"), http.MethodGet, "/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if s.listedAll != 0 {
		t.Error("a non-super-admin was served the full team list")
	}

	got := decodeTeams(t, rec)
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("got %+v, want only the caller's team", got)
	}
	if got[0].Role == nil || *got[0].Role != "developer" {
		t.Errorf("Role = %v, want developer — a member's own listing must carry it", got[0].Role)
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

	got := decodeTeams(t, call(t, s, fakeAuthz{}, outsider(), http.MethodGet, "/teams", ""))

	if len(got) != 2 {
		t.Fatalf("got %d teams, want 2", len(got))
	}
	if got[0].Role == nil || got[1].Role == nil {
		t.Fatal("a role is missing")
	}
	if *got[0].Role != "admin" || *got[1].Role != "developer" {
		t.Errorf("roles = %q, %q; want admin, developer", *got[0].Role, *got[1].Role)
	}
}

// No teams is an answer. A null body makes a client that iterates the result
// crash on something that is not an error.
func TestListReturnsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()

	for name, a := range map[string]Authorizer{
		"super admin": fakeAuthz{super: true},
		"member":      fakeAuthz{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := call(t, &fakeStore{}, a, outsider(), http.MethodGet, "/teams", "")
			if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
				t.Errorf("body = %s, want []", body)
			}
		})
	}
}

func TestListReportsAStoreFailureAsInternal(t *testing.T) {
	t.Parallel()

	for name, a := range map[string]Authorizer{
		"super admin": fakeAuthz{super: true},
		"member":      fakeAuthz{},
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

	rec := call(t, s, fakeAuthz{}, member(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if s.gotTeamID != id {
		t.Errorf("read team %s, want %s", s.gotTeamID, id)
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

	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+id.String(), "")

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

	other := call(t, s, fakeAuthz{}, member(uuid.New(), "admin"), http.MethodGet, "/teams/"+real.String(), "")
	missing := call(t, s, fakeAuthz{}, member(uuid.New(), "admin"), http.MethodGet, "/teams/"+uuid.New().String(), "")

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
	call(t, s, fakeAuthz{}, outsider(), http.MethodGet, "/teams/"+uuid.New().String(), "")

	if s.getTeamCalls != 0 {
		t.Errorf("the store was queried %d times for a caller with no access", s.getTeamCalls)
	}
}

// A super admin asking for an id that is not there gets 404, not 500.
func TestGetIsNotFoundForAMissingTeam(t *testing.T) {
	t.Parallel()

	rec := call(t, &fakeStore{}, fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+uuid.New().String(), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGetRejectsAMalformedID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"abc", "123", "not-a-uuid", "%20"} {
		s := &fakeStore{}
		rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams/"+id, "")

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
	rec := call(t, s, fakeAuthz{}, member(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

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

	for name, tc := range map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, "/teams", `{"name":"platform"}`},
		"list":   {http.MethodGet, "/teams", ""},
		"get":    {http.MethodGet, "/teams/" + uuid.New().String(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, fakeAuthz{super: true}, nil, tc.method, tc.path, tc.body)

			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if s.getTeamCalls != 0 || s.listedAll != 0 || s.listedUser != 0 || len(s.created) != 0 {
				t.Error("the handler reached the store without a user")
			}
		})
	}
}

// The verbs are part of the contract. A route answering a verb it never
// declared is how a read endpoint quietly becomes a write one.
func TestUndeclaredMethodsDoNotAnswer(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	for name, tc := range map[string]struct{ method, path string }{
		"PUT on the collection": {http.MethodPut, "/teams"},
		"DELETE on one team":    {http.MethodDelete, "/teams/" + id},
		"POST on one team":      {http.MethodPost, "/teams/" + id},
		"PATCH on one team":     {http.MethodPatch, "/teams/" + id},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := &fakeStore{}
			rec := call(t, s, fakeAuthz{super: true}, outsider(), tc.method, tc.path, "")

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if s.getTeamCalls != 0 || len(s.created) != 0 {
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
	rec := call(t, s, fakeAuthz{}, member(id, "admin"), http.MethodGet, "/teams/"+id.String(), "")

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

// created_at is only available on the ListTeams path. Publishing it would make
// the response shape depend on who is asking.
func TestTimestampsAreNotPublished(t *testing.T) {
	t.Parallel()

	s := &fakeStore{teams: []db.Team{{ID: uuid.New(), Name: "platform"}}}
	rec := call(t, s, fakeAuthz{super: true}, outsider(), http.MethodGet, "/teams", "")

	if body := rec.Body.String(); strings.Contains(body, "created_at") || strings.Contains(body, "updated_at") {
		t.Errorf("a timestamp reached the wire: %s", body)
	}
}

func TestEveryResponseIsJSON(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	s := &fakeStore{teams: []db.Team{{ID: id, Name: "platform"}}}

	for name, tc := range map[string]struct{ method, path, body string }{
		"create": {http.MethodPost, "/teams", `{"name":"platform"}`},
		"list":   {http.MethodGet, "/teams", ""},
		"get":    {http.MethodGet, "/teams/" + id.String(), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := call(t, s, fakeAuthz{super: true}, outsider(), tc.method, tc.path, tc.body)
			if ct := rec.Result().Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}
