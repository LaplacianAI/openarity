package store

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// rbac.json is the product's defaults, not the last word: an enterprise
// composing its own roles in a dashboard has to survive every deploy. These
// tests hold the four rules that makes true — permissions upserted, only the
// roles named in the file touched, routes replaced wholesale, and the whole
// load atomic.

// scalar reads a single value, so a test can assert on a count without three
// lines of Scan.
func scalar[T any](t *testing.T, s *Store, sql string, args ...any) T {
	t.Helper()

	var v T
	if err := s.pool.QueryRow(t.Context(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %.60s…: %v", sql, err)
	}
	return v
}

func grantsOf(t *testing.T, s *Store, role string) []string {
	t.Helper()

	got, err := s.ListRolePermissions(t.Context(), role)
	if err != nil {
		t.Fatalf("ListRolePermissions(%s): %v", role, err)
	}
	slices.Sort(got)
	return got
}

// withRoutes edits the embedded file for a test that needs a shape the real
// one does not have. Parsing and re-encoding rather than hand-writing JSON
// keeps these tests honest about the format the loader actually reads.
func withRoutes(t *testing.T, edit func(*rbacFile)) []byte {
	t.Helper()

	var f rbacFile
	if err := json.Unmarshal(rbacJSON, &f); err != nil {
		t.Fatalf("unmarshal rbac.json: %v", err)
	}
	edit(&f)

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// The file is the input to everything below, so a bad edit should fail here
// with a sentence rather than downstream as a foreign key violation.
func TestTheShippedFileIsValid(t *testing.T) {
	if _, err := parseRBAC(rbacJSON); err != nil {
		t.Fatalf("rbac.json: %v", err)
	}
}

func TestEveryPermissionCarriesADescription(t *testing.T) {
	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	for _, p := range f.Permissions {
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("%q has no description — a dashboard would show the name", p.Name)
		}
	}
}

// A route naming a permission the file does not define would require something
// nobody can hold, which reads as "forbidden" to everybody.
func TestARouteMayNotNameAnUnknownPermission(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		p := "no:such"
		f.Routes = append(f.Routes, rbacRoute{
			Method: "GET", Path: "/nowhere", Permission: &p, Scope: "team",
		})
	})

	_, err := parseRBAC(data)
	if err == nil {
		t.Fatal("accepted a route requiring a permission that is not defined")
	}
	if !strings.Contains(err.Error(), "no:such") {
		t.Errorf("error does not name the permission: %v", err)
	}
}

func TestARoleMayNotGrantAnUnknownPermission(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		f.Roles[0].Permissions = append(f.Roles[0].Permissions, "report:read")
	})

	_, err := parseRBAC(data)
	if err == nil {
		t.Fatal("accepted a grant of a permission that is not defined")
	}
	if !strings.Contains(err.Error(), "report:read") {
		t.Errorf("error does not name the permission: %v", err)
	}
}

func TestAScopeOutsideTheFiveIsRejected(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		f.Routes = append(f.Routes, rbacRoute{
			Method: "GET", Path: "/nowhere", Scope: "everywhere",
		})
	})

	_, err := parseRBAC(data)
	if err == nil {
		t.Fatal("accepted a scope that maps to no check")
	}
	if !strings.Contains(err.Error(), "everywhere") {
		t.Errorf("error does not name the scope: %v", err)
	}
}

// The two halves have to agree. A team-scoped route with no permission checks
// nothing at all, and it is the worst shape available because the row exists,
// so the startup check is satisfied.
func TestATeamScopedRouteWithoutAPermissionIsRejected(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		f.Routes = append(f.Routes, rbacRoute{
			Method: "GET", Path: "/nowhere", Scope: "team",
		})
	})

	if _, err := parseRBAC(data); err == nil {
		t.Fatal("a team-scoped route was accepted with no permission")
	}
}

func TestAMembershipRouteWithAPermissionIsRejected(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		p := "user:read"
		f.Routes = append(f.Routes, rbacRoute{
			Method: "GET", Path: "/nowhere", Permission: &p, Scope: "member",
		})
	})

	if _, err := parseRBAC(data); err == nil {
		t.Fatal("a member-scoped route named a permission that is never checked")
	}
}

func TestTheSameRouteTwiceIsRejected(t *testing.T) {
	data := withRoutes(t, func(f *rbacFile) {
		f.Routes = append(f.Routes, f.Routes[0])
	})

	_, err := parseRBAC(data)
	if err == nil {
		t.Fatal("accepted the same method and path twice")
	}
	if !strings.Contains(err.Error(), "/teams") {
		t.Errorf("error does not name the route: %v", err)
	}
}

// Malformed JSON must not read as an empty file — that would clear every route
// and leave the startup check to refuse to start for a reason that is nowhere
// near the real one.
func TestMalformedJSONIsAnError(t *testing.T) {
	if _, err := parseRBAC([]byte("{")); err == nil {
		t.Fatal("accepted malformed JSON")
	}
}

// Migrate applies the schema and the file together. Anything less means a
// deployment that migrated but has no route mapping, which the startup check
// reports as a missing route rather than a load that never ran.
func TestMigrateLoadsTheFile(t *testing.T) {
	s := queryStore(t)

	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM route_permissions`); n != len(f.Routes) {
		t.Errorf("route_permissions has %d rows, rbac.json has %d routes", n, len(f.Routes))
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM permissions`); n < len(f.Permissions) {
		t.Errorf("permissions has %d rows, rbac.json defines %d", n, len(f.Permissions))
	}
}

func TestARouteIsLoadedWithItsScopeAndPermission(t *testing.T) {
	s := queryStore(t)

	scope := scalar[string](t, s,
		`SELECT scope FROM route_permissions WHERE method = 'GET' AND path = '/users'`)
	if scope != "any_team" {
		t.Errorf("GET /users scope = %q, want any_team", scope)
	}

	perm := scalar[string](t, s,
		`SELECT permission FROM route_permissions WHERE method = 'GET' AND path = '/users'`)
	if perm != "user:read" {
		t.Errorf("GET /users permission = %q, want user:read", perm)
	}
}

// A route needing no permission stores null rather than an empty string, so
// "requires nothing" and "requires a permission named ”" stay distinct.
func TestARouteThatNeedsNoPermissionStoresNull(t *testing.T) {
	s := queryStore(t)

	null := scalar[bool](t, s,
		`SELECT permission IS NULL FROM route_permissions WHERE method = 'GET' AND path = '/whoami'`)
	if !null {
		t.Error("GET /whoami stored a permission")
	}
}

// Every boot runs this. A second load that changes anything means restarting
// the brain is a write, and two brains booting together would fight.
func TestLoadingTwiceChangesNothing(t *testing.T) {
	s := queryStore(t)

	before := snapshot(t, s)
	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if after := snapshot(t, s); after != before {
		t.Errorf("a second load changed the tables:\n%s\nwant\n%s", after, before)
	}
}

// snapshot reduces the three tables to a comparable string, so an idempotency
// failure reports what moved rather than "not equal".
func snapshot(t *testing.T, s *Store) string {
	t.Helper()

	return scalar[string](t, s, `
		SELECT
			(SELECT coalesce(string_agg(name || '=' || description, ',' ORDER BY name), '') FROM permissions)
			|| ' | ' ||
			(SELECT coalesce(string_agg(role || ':' || action, ',' ORDER BY role, action), '') FROM role_permissions)
			|| ' | ' ||
			(SELECT coalesce(string_agg(method || path || coalesce(permission, '-') || scope, ',' ORDER BY method, path), '') FROM route_permissions)`)
}

func TestADescriptionEditedInTheDatabaseIsRestored(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `UPDATE permissions SET description = 'nonsense' WHERE name = 'user:read'`)

	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("LoadRBAC: %v", err)
	}

	got := scalar[string](t, s, `SELECT description FROM permissions WHERE name = 'user:read'`)
	if got == "nonsense" || got == "" {
		t.Errorf("description = %q, want the one from rbac.json", got)
	}
}

// A permission the file no longer mentions stays. An enterprise may have
// granted it, and the foreign key would refuse the delete anyway — so the
// loader must not try and fail the whole load.
func TestAPermissionTheFileDoesNotMentionSurvives(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO permissions (name, description) VALUES ('report:read', 'Read reports')`)

	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("LoadRBAC: %v", err)
	}

	if n := scalar[int](t, s, `SELECT count(*) FROM permissions WHERE name = 'report:read'`); n != 1 {
		t.Error("the loader deleted a permission it did not create")
	}
}

// The roles the product ships are its own: a grant added by hand is not a
// customisation, it is drift, and the file is what says what admin means.
func TestARoleInTheFileGetsExactlyTheGrantsItLists(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO permissions (name) VALUES ('report:read')`)
	exec(t, s, `INSERT INTO role_permissions (role, action) VALUES ('member', 'report:read')`)
	exec(t, s, `DELETE FROM role_permissions WHERE role = 'member' AND action = 'tool:write'`)

	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("LoadRBAC: %v", err)
	}

	want := []string{"agent:write", "tool:write"}
	if got := grantsOf(t, s, "member"); !slices.Equal(got, want) {
		t.Errorf("member grants = %v, want %v", got, want)
	}
}

// And the other half of the same rule: a role the file never names belongs to
// whoever made it. Rewriting it would undo an enterprise's own work on every
// deploy, which is the failure this whole design exists to avoid.
func TestARoleTheFileDoesNotNameIsUntouched(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO roles (name, description) VALUES ('auditor', 'Reads everything')`)
	exec(t, s, `INSERT INTO role_permissions (role, action) VALUES ('auditor', 'user:read')`)

	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("LoadRBAC: %v", err)
	}

	if got := grantsOf(t, s, "auditor"); !slices.Equal(got, []string{"user:read"}) {
		t.Errorf("auditor grants = %v, want [user:read]", got)
	}

	desc := scalar[string](t, s, `SELECT description FROM roles WHERE name = 'auditor'`)
	if desc != "Reads everything" {
		t.Errorf("auditor description = %q, want the operator's", desc)
	}
}

// Routes come from code, so a row for a route the mux no longer serves is
// stale rather than somebody's configuration. Left behind, it would satisfy
// the startup check for a route that does not exist and hide the removal.
func TestAStaleRouteIsRemoved(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/removed-last-release', 'user:read', 'any_team')`)

	if err := s.LoadRBAC(t.Context()); err != nil {
		t.Fatalf("LoadRBAC: %v", err)
	}

	n := scalar[int](t, s,
		`SELECT count(*) FROM route_permissions WHERE path = '/removed-last-release'`)
	if n != 0 {
		t.Error("a route the file no longer maps survived the load")
	}
}

// Half a load is worse than none: permissions and grants applied without the
// routes leaves the mapping from the previous release guarding the new one.
func TestAFailedLoadChangesNothing(t *testing.T) {
	s := queryStore(t)

	// Make the last step of the load fail, after the permissions and grants
	// have already been written inside the transaction.
	exec(t, s, `ALTER TABLE route_permissions RENAME TO route_permissions_moved`)
	exec(t, s, `DELETE FROM role_permissions WHERE role = 'admin' AND action = 'user:read'`)

	if err := s.LoadRBAC(t.Context()); err == nil {
		t.Fatal("LoadRBAC succeeded with no route_permissions table")
	}

	if got := grantsOf(t, s, "admin"); slices.Contains(got, "user:read") {
		t.Errorf("the grant was committed despite the failure: %v", got)
	}
}

// The loader reports the file it could not apply. Without this the operator
// sees a foreign key violation and has to guess which of three inputs it came
// from.
func TestLoadRejectsAFileItCannotParse(t *testing.T) {
	s := queryStore(t)

	err := s.loadRBAC(t.Context(), []byte(`{"permissions": [`))
	if err == nil {
		t.Fatal("loaded a file that does not parse")
	}
	if !strings.Contains(err.Error(), "rbac") {
		t.Errorf("error does not say what failed to load: %v", err)
	}
}

// A route requiring a permission no shipped role holds is a route nobody can
// reach in a fresh deployment. Nothing fails, nothing logs — every request
// simply gets a 403, and the cause is three files away. This is the test that
// replaced the one asking whether every Go constant was granted; the file is
// the vocabulary now, so the file is what gets checked.
func TestEveryPermissionARouteRequiresIsHeldBySomeRole(t *testing.T) {
	s := queryStore(t)

	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	for _, rt := range f.Routes {
		if rt.Permission == nil {
			continue
		}

		holders := scalar[int](t, s,
			`SELECT count(*) FROM role_permissions WHERE action = $1`, *rt.Permission)
		if holders == 0 {
			t.Errorf("%s %s requires %q, which no role holds — the route is unreachable",
				rt.Method, rt.Path, *rt.Permission)
		}
	}
}

// admin is "full access within a team". A permission it does not hold is
// either a mistake in the file or a sign the description is no longer true,
// and both are worth failing over.
func TestAdminHoldsEveryPermissionTheFileDefines(t *testing.T) {
	s := queryStore(t)

	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	granted := grantsOf(t, s, "admin")
	for _, p := range f.Permissions {
		if !slices.Contains(granted, p.Name) {
			t.Errorf("admin does not hold %q", p.Name)
		}
	}
}

// The mapping itself, pinned. Every other test here checks that the file is
// internally consistent and that the loader applies it faithfully — none of
// them would notice a route being given the wrong scope, which is the change
// most likely to be both quiet and serious.
//
// any_team on a route with {id} in it is the specific mistake this catches: an
// admin of one team passes it, so it would grant them every team.
func TestTheRouteMappingIsWhatWeIntend(t *testing.T) {
	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	want := map[string]string{
		"POST /teams":                         "super_admin",
		"GET /teams":                          "authenticated",
		"GET /teams/{id}":                     "member",
		"GET /teams/{id}/members":             "member",
		"POST /teams/{id}/members":            "team membership:write",
		"DELETE /teams/{id}/members/{userID}": "team membership:write",

		// Listing is `member` and not `team channel:read`: a channel id is
		// the routing key in its own hook URL, not a secret, and everyone in
		// the team needs to see which channels exist. Connecting one is an
		// admin act, so writes carry a permission.
		"GET /teams/{id}/channels":                "member",
		"POST /teams/{id}/channels":               "team channel:write",
		"DELETE /teams/{id}/channels/{channelID}": "team channel:write",

		// Senders are all channel:write, including the reads. Approving a
		// stranger grants them the right to instruct an agent as a named
		// user, which is the same kind of act as connecting the channel —
		// and the pending queue is only actionable by whoever can approve.
		// It is also attacker-controlled text: anyone who finds the hook URL
		// can put fifty display names of their choosing in front of whoever
		// reads it, so it goes to the smallest audience that can act on it.
		"GET /teams/{id}/channels/{channelID}/senders/pending": "team channel:write",
		"GET /teams/{id}/channels/{channelID}/senders":         "team channel:write",
		"POST /teams/{id}/channels/{channelID}/senders":        "team channel:write",
		"DELETE /teams/{id}/channels/{channelID}/senders":      "team channel:write",

		// Reading is `member`, unlike the sender routes above. A session is
		// the conversation the team is already having in the provider — the
		// same words, in a second place. Requiring channel:write would mean
		// an admin reads every conversation on everybody else's behalf.
		"GET /teams/{id}/channels/{channelID}/sessions": "member",
		"GET /teams/{id}/sessions":                      "member",
		"GET /teams/{id}/sessions/{sessionID}/messages": "member",

		// Attachments are the session's, on purpose. There is deliberately no
		// attachment:read permission: a role holding one without session:read
		// could read the file sent in a private conversation it may not open.
		// Deriving the check from the session — the same `visible` the message
		// route uses — makes that combination unrepresentable rather than
		// merely unconfigured.
		"GET /teams/{id}/sessions/{sessionID}/attachments":                "member",
		"GET /teams/{id}/sessions/{sessionID}/attachments/{attachmentID}": "member",

		"GET /users":  "any_team user:read",
		"GET /whoami": "authenticated",
	}

	got := make(map[string]string, len(f.Routes))
	for _, rt := range f.Routes {
		mapping := rt.Scope
		if rt.Permission != nil {
			mapping += " " + *rt.Permission
		}
		got[rt.Method+" "+rt.Path] = mapping
	}

	for route, mapping := range want {
		switch actual, mapped := got[route]; {
		case !mapped:
			t.Errorf("%s is no longer mapped", route)
		case actual != mapping:
			t.Errorf("%s is %q, want %q", route, actual, mapping)
		}
	}
	for route := range got {
		if _, expected := want[route]; !expected {
			t.Errorf("%s was added without a decision here", route)
		}
	}
}

// A route with a team in its path must never be any_team. The check above
// would catch it, but only for the eight routes named there — this one holds
// for every route added later.
func TestNoRouteWithATeamInItsPathIsAnyTeam(t *testing.T) {
	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}

	for _, rt := range f.Routes {
		if rt.Scope == "any_team" && strings.Contains(rt.Path, "{id}") {
			t.Errorf("%s %s is any_team with a team in its path — an admin of "+
				"any one team would pass it, and so reach every team",
				rt.Method, rt.Path)
		}
	}
}
