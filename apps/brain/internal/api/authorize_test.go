package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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

// fakeTeams says which teams are deleted and records every team it was asked
// about, so a test can tell a guard that never looked from one that looked at
// the wrong team.
type fakeTeams struct {
	deleted map[uuid.UUID]bool
	err     error
	asked   []uuid.UUID
}

func (f *fakeTeams) TeamDeleted(_ context.Context, id uuid.UUID) (bool, error) {
	f.asked = append(f.asked, id)
	return f.deleted[id], f.err
}

// guardFor builds a guard holding exactly one route, which is all any of these
// tests needs.
func guardFor(t *testing.T, method, path, scope string, permission *string, a Authorizer) *Guard {
	t.Helper()

	routes := authz.NewRoutes()
	if err := routes.Add(method, path, scope, permission); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return NewGuard(routes, a, &fakeTeams{}, discardLogger())
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

	g := NewGuard(authz.NewRoutes(), &fakeAuthorizer{}, &fakeTeams{}, discardLogger())

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

	g := NewGuard(routes, &fakeAuthorizer{}, &fakeTeams{}, discardLogger())
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

// --- a deleted team is read-only ------------------------------------------

const deletedTeamRefusal = "team is deleted; restore it first"

// The two writes a deleted team still takes: deleting it again keeps its
// purge date, and restoring it is the whole point of keeping it.
var exemptWhenDeleted = map[string]bool{
	"DELETE /teams/{id}":       true,
	"POST /teams/{id}/restore": true,
}

type shippedRoute struct {
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Scope      string  `json:"scope"`
	Permission *string `json:"permission"`
}

// shippedRoutes reads the rbac.json that ships, so a route added tomorrow is
// covered by these tests without anyone listing it here.
func shippedRoutes(t *testing.T) (authz.Routes, []shippedRoute) {
	t.Helper()

	data, err := os.ReadFile("../store/rbac.json")
	if err != nil {
		t.Fatalf("read rbac.json: %v", err)
	}
	var f struct {
		Routes []shippedRoute `json:"routes"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse rbac.json: %v", err)
	}

	routes := authz.NewRoutes()
	for _, r := range f.Routes {
		if err := routes.Add(r.Method, r.Path, r.Scope, r.Permission); err != nil {
			t.Fatalf("Add %s %s: %v", r.Method, r.Path, err)
		}
	}
	return routes, f.Routes
}

var pathValue = regexp.MustCompile(`\{[^}]+\}`)

// teamTarget fills a route's pattern: {id} with the team, anything else with
// a fresh uuid.
func teamTarget(pattern string, team uuid.UUID) string {
	return pathValue.ReplaceAllStringFunc(pattern, func(v string) string {
		if v == "{"+teamPathValue+"}" {
			return team.String()
		}
		return uuid.NewString()
	})
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// A super admin may look at a deleted team, delete it again or restore it,
// and nothing else: every other write would change a team that is waiting to
// be purged, and one that runs an agent would spend money on it.
func TestADeletedTeamIsReadOnlyToASuperAdmin(t *testing.T) {
	t.Parallel()

	routes, shipped := shippedRoutes(t)
	team := uuid.New()

	var reads, writes, exempt int
	for _, sr := range shipped {
		if !strings.Contains(sr.Path, "{id}") {
			continue
		}
		key := authz.RouteKey(sr.Method, sr.Path)

		g := NewGuard(routes, &fakeAuthorizer{super: true, allowed: true},
			&fakeTeams{deleted: map[uuid.UUID]bool{team: true}}, discardLogger())
		ran := false
		rec := run(t, g, sr.Method, sr.Path, teamTarget(sr.Path, team), testUser(),
			func(w http.ResponseWriter, _ *http.Request) { ran = true; w.WriteHeader(http.StatusOK) })

		switch {
		case isRead(sr.Method) || exemptWhenDeleted[key]:
			if isRead(sr.Method) {
				reads++
			} else {
				exempt++
			}
			if rec.Code != http.StatusOK || !ran {
				t.Errorf("%s: status = %d, ran = %v, want it let through", key, rec.Code, ran)
			}
		default:
			writes++
			if rec.Code != http.StatusConflict {
				t.Errorf("%s: status = %d, want 409", key, rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != deletedTeamRefusal {
				t.Errorf("%s: body = %q, want %q", key, got, deletedTeamRefusal)
			}
			if ran {
				t.Errorf("%s: the handler ran on a deleted team", key)
			}
		}
	}

	// A loop that matched nothing would pass every assertion above.
	if reads == 0 || writes == 0 || exempt != len(exemptWhenDeleted) {
		t.Errorf("checked %d reads, %d writes and %d exempt routes; want some of each and both exemptions",
			reads, writes, exempt)
	}
}

// The same routes on a live team are untouched, and a write asks about the
// team in its path rather than any team.
func TestALiveTeamIsWritableToASuperAdmin(t *testing.T) {
	t.Parallel()

	routes, shipped := shippedRoutes(t)
	team := uuid.New()

	for _, sr := range shipped {
		if !strings.Contains(sr.Path, "{id}") {
			continue
		}
		key := authz.RouteKey(sr.Method, sr.Path)

		teams := &fakeTeams{}
		g := NewGuard(routes, &fakeAuthorizer{super: true, allowed: true}, teams, discardLogger())
		ran := false
		rec := run(t, g, sr.Method, sr.Path, teamTarget(sr.Path, team), testUser(),
			func(w http.ResponseWriter, _ *http.Request) { ran = true; w.WriteHeader(http.StatusOK) })

		if rec.Code != http.StatusOK || !ran {
			t.Errorf("%s: status = %d, ran = %v, want it let through", key, rec.Code, ran)
		}
		if isRead(sr.Method) || exemptWhenDeleted[key] {
			continue
		}
		if len(teams.asked) == 0 {
			t.Errorf("%s: a write never asked whether its team was deleted", key)
		}
		for _, id := range teams.asked {
			if id != team {
				t.Errorf("%s: asked about team %s, want the one in the path %s", key, id, team)
			}
		}
	}
}

// Not knowing is not the same as live: a write that cannot be checked is
// refused, under every scope a team route can have.
func TestATeamStateFailureIsAnInternalError(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		name       string
		permission *string
	}{
		{"member", nil},
		{"team", ptr("membership:write")},
		{"super_admin", nil},
	} {
		routes := authz.NewRoutes()
		if err := routes.Add("POST", "/teams/{id}/members", scope.name, scope.permission); err != nil {
			t.Fatalf("Add: %v", err)
		}
		g := NewGuard(routes, &fakeAuthorizer{super: true, allowed: true},
			&fakeTeams{err: errCheckFailed}, discardLogger())

		ran := false
		rec := run(t, g, "POST", "/teams/{id}/members", "/teams/"+uuid.NewString()+"/members", testUser(),
			func(http.ResponseWriter, *http.Request) { ran = true })

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", scope.name, rec.Code)
		}
		if ran {
			t.Errorf("%s: the handler ran when the team's state was unknown", scope.name)
		}
	}
}

// A member never reaches a deleted team at all: ListUserTeams leaves it out
// of their memberships, so the member and team checks already answer 404 or
// 403. Asking again would be a query on every request for nothing.
func TestAMemberNeverReadsTeamState(t *testing.T) {
	t.Parallel()

	routes, shipped := shippedRoutes(t)
	team := uuid.New()
	u := testUser(auth.Membership{TeamID: team, Role: "admin"})

	teams := &fakeTeams{deleted: map[uuid.UUID]bool{team: true}}
	g := NewGuard(routes, &fakeAuthorizer{super: false, allowed: true}, teams, discardLogger())

	for _, sr := range shipped {
		if !strings.Contains(sr.Path, "{id}") {
			continue
		}
		run(t, g, sr.Method, sr.Path, teamTarget(sr.Path, team), u, respondOK)
	}

	if len(teams.asked) != 0 {
		t.Errorf("a member's requests read the team's state %d times, want none", len(teams.asked))
	}
}

// A route with no team in its path has no team to be deleted.
func TestARouteWithNoTeamNeverReadsTeamState(t *testing.T) {
	t.Parallel()

	teams := &fakeTeams{}
	routes := authz.NewRoutes()
	if err := routes.Add("POST", "/teams", "super_admin", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	g := NewGuard(routes, &fakeAuthorizer{super: true}, teams, discardLogger())

	rec := run(t, g, "POST", "/teams", "/teams", testUser(), respondOK)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if len(teams.asked) != 0 {
		t.Errorf("read the state of %v for a route with no team", teams.asked)
	}
}
