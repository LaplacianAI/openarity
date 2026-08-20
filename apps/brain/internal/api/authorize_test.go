package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
)

// The guard is the only thing between a request and a handler, and which check
// it runs comes from a table rather than from the registration line. So these
// tests are mostly about the switch: every scope reaches the check it names,
// nothing reaches a handler it should not, and a route with no row cannot be
// registered at all.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var errCheckFailed = errors.New("connection refused")

// fakeAuthorizer answers every question and records what it was asked.
type fakeAuthorizer struct {
	allowed bool
	err     error
	super   bool

	action  authz.Action
	teamID  uuid.UUID
	calls   int
	inAny   int
	inTeam  int
	superAt int
}

func (f *fakeAuthorizer) IsSuperAdmin(*auth.User) bool {
	f.superAt++
	return f.super
}

func (f *fakeAuthorizer) Can(
	_ context.Context, _ *auth.User, a authz.Action, r authz.Resource,
) (bool, error) {
	f.calls++
	f.inTeam++
	f.action, f.teamID = a, r.TeamID
	return f.allowed, f.err
}

func (f *fakeAuthorizer) CanInAnyTeam(
	_ context.Context, _ *auth.User, a authz.Action,
) (bool, error) {
	f.calls++
	f.inAny++
	f.action = a
	return f.allowed, f.err
}

// guardFor builds a guard holding exactly one route, which is all any of these
// tests needs.
func guardFor(t *testing.T, method, path, scope string, permission *string, a Authorizer) *Guard {
	t.Helper()

	routes := authz.NewRoutes()
	if err := routes.Add(method, path, scope, permission); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return NewGuard(routes, a, discardLogger())
}

func ptr(s string) *string { return &s }

func testUser(teams ...auth.Membership) *auth.User {
	return &auth.User{ID: uuid.New(), Subject: "alice", Teams: teams}
}

// run sends one request through a mux, so PathValue resolves — which the
// team-scoped and member-scoped checks depend on. (serve is taken by
// router_test.go.)
func run(
	t *testing.T, g *Guard, method, pattern, target string,
	u *auth.User, h http.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()

	guarded, err := g.Wrap(authz.RouteKey(method, pattern), h)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), u))
	}

	mux := http.NewServeMux()
	mux.HandleFunc(method+" "+pattern, guarded)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func respondOK(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// A route with no row cannot be wrapped, and the failure is at startup with
// the route named. The alternative — treating a missing row as "no check
// needed" — is how a route ships open and nobody finds out.
func TestAnUnmappedRouteCannotBeRegistered(t *testing.T) {
	t.Parallel()

	g := NewGuard(authz.NewRoutes(), &fakeAuthorizer{}, discardLogger())

	_, err := g.Wrap("GET /forgotten", respondOK)
	if err == nil {
		t.Fatal("wrapped a route with no mapping")
	}
	if !strings.Contains(err.Error(), "GET /forgotten") {
		t.Errorf("error does not name the route: %v", err)
	}
}

// The auth middleware already established the caller, so this scope adds
// nothing but the guarantee that it ran.
func TestAuthenticatedRunsTheHandlerAndAsksNobody(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{}
	g := guardFor(t, "GET", "/whoami", "authenticated", nil, a)

	rec := run(t, g, "GET", "/whoami", "/whoami", testUser(), respondOK)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if a.calls != 0 {
		t.Error("an authenticated route consulted the authorizer")
	}
}

// No user in the context means the guard is mounted outside the resolver.
// That is a wiring bug, and answering 403 would send someone looking at roles
// instead of at the middleware order.
func TestNoCallerIsAnInternalError(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		name       string
		permission *string
	}{
		{"authenticated", nil},
		{"member", nil},
		{"super_admin", nil},
		{"team", ptr("membership:write")},
		{"any_team", ptr("user:read")},
	} {
		a := &fakeAuthorizer{allowed: true, super: true}
		g := guardFor(t, "GET", "/teams/{id}", scope.name, scope.permission, a)

		ran := false
		rec := run(t, g, "GET", "/teams/{id}", "/teams/"+uuid.New().String(), nil,
			func(http.ResponseWriter, *http.Request) { ran = true })

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", scope.name, rec.Code)
		}
		if ran {
			t.Errorf("%s: the handler ran with no caller", scope.name)
		}
	}
}

func TestSuperAdminAllowsASuperAdmin(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{super: true}
	g := guardFor(t, "POST", "/teams", "super_admin", nil, a)

	rec := run(t, g, "POST", "/teams", "/teams", testUser(), respondOK)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSuperAdminRefusesEverybodyElse(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{super: false, allowed: true}
	g := guardFor(t, "POST", "/teams", "super_admin", nil, a)

	ran := false
	rec := run(t, g, "POST", "/teams", "/teams", testUser(),
		func(http.ResponseWriter, *http.Request) { ran = true })

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if ran {
		t.Error("the handler ran for a non-super-admin")
	}
	// allowed is true, so a guard that fell through to a permission check
	// would have let this request past.
	if a.calls != 0 {
		t.Error("a super-admin route consulted the permission model")
	}
}

func TestMemberAllowsAMemberOfTheTeamInThePath(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	u := testUser(auth.Membership{TeamID: team, Role: "member"})
	g := guardFor(t, "GET", "/teams/{id}", "member", nil, &fakeAuthorizer{})

	rec := run(t, g, "GET", "/teams/{id}", "/teams/"+team.String(), u, respondOK)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// 404, not 403. Telling somebody a team exists is itself a leak — with 403 an
// outsider can enumerate team ids and learn which ones are real.
func TestMemberIsNotFoundForANonMember(t *testing.T) {
	t.Parallel()

	other := uuid.New()
	u := testUser(auth.Membership{TeamID: uuid.New(), Role: "admin"})
	g := guardFor(t, "GET", "/teams/{id}", "member", nil, &fakeAuthorizer{})

	ran := false
	rec := run(t, g, "GET", "/teams/{id}", "/teams/"+other.String(), u,
		func(http.ResponseWriter, *http.Request) { ran = true })

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — 403 confirms the team exists", rec.Code)
	}
	if ran {
		t.Error("the handler ran for a non-member")
	}
}

func TestMemberLetsASuperAdminThrough(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{super: true}
	g := guardFor(t, "GET", "/teams/{id}", "member", nil, a)

	rec := run(t, g, "GET", "/teams/{id}", "/teams/"+uuid.New().String(), testUser(), respondOK)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a super admin belongs to every team", rec.Code)
	}
}

func TestTeamAsksAboutTheTeamInThePathAndTheRoutesPermission(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{allowed: true}
	g := guardFor(t, "POST", "/teams/{id}/members", "team", ptr("membership:write"), a)

	team := uuid.New()
	rec := run(t, g, "POST", "/teams/{id}/members", "/teams/"+team.String()+"/members",
		testUser(), respondOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if a.teamID != team {
		t.Errorf("asked about team %s, want %s", a.teamID, team)
	}
	if a.action != authz.Action("membership:write") {
		t.Errorf("asked about %q, want membership:write", a.action)
	}
	// The strictly weaker check must not be the one that ran: an admin of any
	// one team passes CanInAnyTeam, which would grant them every team.
	if a.inAny != 0 {
		t.Error("a team-scoped route used CanInAnyTeam")
	}
}

func TestAnyTeamUsesTheUnscopedCheck(t *testing.T) {
	t.Parallel()

	a := &fakeAuthorizer{allowed: true}
	g := guardFor(t, "GET", "/users", "any_team", ptr("user:read"), a)

	rec := run(t, g, "GET", "/users", "/users", testUser(), respondOK)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if a.inAny != 1 {
		t.Errorf("CanInAnyTeam ran %d times, want 1", a.inAny)
	}
	if a.action != authz.Action("user:read") {
		t.Errorf("asked about %q, want user:read", a.action)
	}
}

func TestAPermissionDenialDoesNotRunTheHandler(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		scope      string
		pattern    string
		target     string
		permission *string
	}{
		{"team", "/teams/{id}/members", "/teams/" + uuid.New().String() + "/members", ptr("membership:write")},
		{"any_team", "/users", "/users", ptr("user:read")},
	} {
		a := &fakeAuthorizer{allowed: false}
		g := guardFor(t, "GET", tc.pattern, tc.scope, tc.permission, a)

		ran := false
		rec := run(t, g, "GET", tc.pattern, tc.target, testUser(),
			func(http.ResponseWriter, *http.Request) { ran = true })

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", tc.scope, rec.Code)
		}
		if ran {
			t.Errorf("%s: the handler ran despite the denial", tc.scope)
		}
	}
}

// A failed permission read is unknown, not denied. Collapsing them makes a
// database blip look like a permissions problem and sends whoever is on call
// to the wrong system.
func TestAFailedCheckIsAnInternalError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		scope      string
		pattern    string
		target     string
		permission *string
	}{
		{"team", "/teams/{id}/members", "/teams/" + uuid.New().String() + "/members", ptr("membership:write")},
		{"any_team", "/users", "/users", ptr("user:read")},
	} {
		a := &fakeAuthorizer{err: errCheckFailed}
		g := guardFor(t, "GET", tc.pattern, tc.scope, tc.permission, a)

		ran := false
		rec := run(t, g, "GET", tc.pattern, tc.target, testUser(),
			func(http.ResponseWriter, *http.Request) { ran = true })

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", tc.scope, rec.Code)
		}
		if ran {
			t.Errorf("%s: the handler ran after the check failed", tc.scope)
		}
	}
}

// An unparseable team is the caller's mistake, not a denial, and it must be
// refused before any permission question is asked.
func TestANonUUIDTeamIsABadRequest(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		scope      string
		permission *string
	}{
		{"member", nil},
		{"team", ptr("membership:write")},
	} {
		a := &fakeAuthorizer{allowed: true, super: true}
		g := guardFor(t, "GET", "/teams/{id}", tc.scope, tc.permission, a)

		ran := false
		rec := run(t, g, "GET", "/teams/{id}", "/teams/not-a-uuid", testUser(),
			func(http.ResponseWriter, *http.Request) { ran = true })

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.scope, rec.Code)
		}
		if ran {
			t.Errorf("%s: the handler ran with an unparseable team id", tc.scope)
		}
		if a.calls != 0 {
			t.Errorf("%s: the authorizer was asked about an unparseable team", tc.scope)
		}
	}
}

// The guard already parsed the id to run the check. Handing it on means the
// handler cannot parse it a second time and get a different answer.
func TestTheTeamIsPassedToTheHandler(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		scope      string
		permission *string
	}{
		{"member", nil},
		{"team", ptr("membership:write")},
	} {
		a := &fakeAuthorizer{allowed: true, super: true}
		g := guardFor(t, "GET", "/teams/{id}", tc.scope, tc.permission, a)

		want := uuid.New()
		var got uuid.UUID
		var found bool

		run(t, g, "GET", "/teams/{id}", "/teams/"+want.String(), testUser(),
			func(_ http.ResponseWriter, r *http.Request) {
				got, found = TeamFrom(r.Context())
			})

		if !found {
			t.Errorf("%s: the handler was given no team", tc.scope)
		}
		if got != want {
			t.Errorf("%s: handler got team %s, want %s", tc.scope, got, want)
		}
	}
}

// A route with no team in its path must not carry one, or a handler could read
// a stale value from a previous request's context and scope a query to it.
func TestARouteWithNoTeamCarriesNone(t *testing.T) {
	t.Parallel()

	g := guardFor(t, "GET", "/users", "any_team", ptr("user:read"), &fakeAuthorizer{allowed: true})

	var found bool
	run(t, g, "GET", "/users", "/users", testUser(),
		func(_ http.ResponseWriter, r *http.Request) {
			_, found = TeamFrom(r.Context())
		})

	if found {
		t.Error("a route with no team in its path carried one")
	}
}

// The denial body must not say what was missing. A caller learning which
// permission a route wants is being handed a map of the permission model.
func TestADenialNamesNothing(t *testing.T) {
	t.Parallel()

	g := guardFor(t, "GET", "/users", "any_team", ptr("user:read"), &fakeAuthorizer{allowed: false})

	rec := run(t, g, "GET", "/users", "/users", testUser(),
		func(http.ResponseWriter, *http.Request) {})

	if body := rec.Body.String(); strings.Contains(body, "user:read") {
		t.Errorf("the denial named the permission: %q", body)
	}
}

// The other direction of the startup check. A row for a route the mux no
// longer serves means rbac.json is stale — harmless at runtime, and a sign
// that a route was deleted and the file was not.
func TestUnusedReportsRoutesNoRouterRegistered(t *testing.T) {
	t.Parallel()

	routes := authz.NewRoutes()
	for _, r := range []struct {
		method, path string
	}{{"GET", "/users"}, {"GET", "/whoami"}, {"GET", "/gone"}} {
		if err := routes.Add(r.method, r.path, "authenticated", nil); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	g := NewGuard(routes, &fakeAuthorizer{}, discardLogger())
	for _, key := range []string{"GET /users", "GET /whoami"} {
		if _, err := g.Wrap(key, respondOK); err != nil {
			t.Fatalf("Wrap(%s): %v", key, err)
		}
	}

	unused := g.Unused()
	if !slices.Equal(unused, []string{"GET /gone"}) {
		t.Errorf("Unused() = %v, want [GET /gone]", unused)
	}
}

func TestUnusedIsEmptyWhenEveryRouteIsRegistered(t *testing.T) {
	t.Parallel()

	g := guardFor(t, "GET", "/users", "any_team", ptr("user:read"), &fakeAuthorizer{})
	if _, err := g.Wrap("GET /users", respondOK); err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if unused := g.Unused(); len(unused) != 0 {
		t.Errorf("Unused() = %v, want none", unused)
	}
}
