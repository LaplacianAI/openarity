package authz

import (
	"strings"
	"testing"
)

// Scopes are code and permissions are data — that asymmetry is the whole
// design. Adding a permission is a row, because nothing in Go has to change
// to check it. Adding a scope is a new check, so it is a constant and a case
// in a switch. These tests hold that line at the point the two meet: the
// table the guard reads.

func ptr(s string) *string { return &s }

func TestARouteIsFoundByItsMethodAndPath(t *testing.T) {
	rs := NewRoutes()

	if err := rs.Add("GET", "/users", "any_team", ptr("user:read")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := rs.Lookup("GET", "/users")
	if !ok {
		t.Fatal("GET /users is not in the table")
	}
	if got.Scope != ScopeAnyTeam {
		t.Errorf("scope = %q, want %q", got.Scope, ScopeAnyTeam)
	}
	if got.Permission != Action("user:read") {
		t.Errorf("permission = %q, want user:read", got.Permission)
	}
}

// The same path under two methods is two routes, and they may differ in every
// respect — GET /teams needs a token and POST /teams needs a super admin.
func TestMethodIsPartOfTheKey(t *testing.T) {
	rs := NewRoutes()

	if err := rs.Add("GET", "/teams", "authenticated", nil); err != nil {
		t.Fatalf("Add GET: %v", err)
	}
	if err := rs.Add("POST", "/teams", "super_admin", nil); err != nil {
		t.Fatalf("Add POST: %v", err)
	}

	get, _ := rs.Lookup("GET", "/teams")
	post, _ := rs.Lookup("POST", "/teams")
	if get.Scope == post.Scope {
		t.Errorf("both methods resolved to %q", get.Scope)
	}
}

func TestAnUnmappedRouteIsNotFound(t *testing.T) {
	rs := NewRoutes()

	if _, ok := rs.Lookup("GET", "/nowhere"); ok {
		t.Error("an unmapped route was found")
	}
}

// The rows come from the database, which constrains all of this already. The
// guard still refuses to build a table it cannot interpret: a scope it does
// not recognise must not arrive at a switch whose default it would fall
// through, and "fell through" reads as "allowed" if anyone writes it wrong.
func TestAnUnknownScopeIsRejected(t *testing.T) {
	rs := NewRoutes()

	err := rs.Add("GET", "/nowhere", "everywhere", nil)
	if err == nil {
		t.Fatal("accepted a scope with no check behind it")
	}
	if !strings.Contains(err.Error(), "everywhere") {
		t.Errorf("error does not name the scope: %v", err)
	}
}

func TestEveryScopeIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		scope      string
		permission *string
	}{
		{"authenticated", nil},
		{"member", nil},
		{"super_admin", nil},
		{"team", ptr("membership:write")},
		{"any_team", ptr("user:read")},
	} {
		rs := NewRoutes()
		if err := rs.Add("GET", "/x", tc.scope, tc.permission); err != nil {
			t.Errorf("scope %q rejected: %v", tc.scope, err)
		}
	}
}

// A team-scoped route with no permission would reach Can with an empty action,
// which no role holds — so it would deny everybody rather than check nothing.
// Either way it is not what was meant, and it must not start.
func TestATeamScopeWithoutAPermissionIsRejected(t *testing.T) {
	rs := NewRoutes()

	if err := rs.Add("GET", "/nowhere", "team", nil); err == nil {
		t.Fatal("a team-scoped route was accepted with no permission")
	}
}

func TestAScopeThatChecksNoPermissionMustNotCarryOne(t *testing.T) {
	rs := NewRoutes()

	err := rs.Add("GET", "/nowhere", "member", ptr("user:read"))
	if err == nil {
		t.Fatal("a member-scoped route named a permission that is never checked")
	}
	if !strings.Contains(err.Error(), "user:read") {
		t.Errorf("error does not name the permission: %v", err)
	}
}

// Two rows for the same route would make the guard depend on load order.
// The database's primary key prevents it; this prevents a caller building the
// table from two sources.
func TestTheSameRouteTwiceIsRejected(t *testing.T) {
	rs := NewRoutes()

	if err := rs.Add("GET", "/users", "any_team", ptr("user:read")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := rs.Add("GET", "/users", "authenticated", nil)
	if err == nil {
		t.Fatal("the same route was added twice")
	}
	if !strings.Contains(err.Error(), "/users") {
		t.Errorf("error does not name the route: %v", err)
	}
}

// Keys stay in the mux's own form, so the startup check compares strings the
// router already produces rather than reformatting either side.
func TestKeysMatchTheMuxPatternForm(t *testing.T) {
	rs := NewRoutes()

	if err := rs.Add("DELETE", "/teams/{id}/members/{userID}", "team", ptr("membership:write")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := "DELETE /teams/{id}/members/{userID}"
	if _, ok := rs[want]; !ok {
		t.Errorf("no key %q; got %v", want, rs.Keys())
	}
}

// Keys is what the startup check reports when rbac.json maps a route the mux
// does not serve, so it has to be stable rather than map-ordered.
func TestKeysAreOrdered(t *testing.T) {
	rs := NewRoutes()

	for _, p := range []string{"/z", "/a", "/m"} {
		if err := rs.Add("GET", p, "authenticated", nil); err != nil {
			t.Fatalf("Add %s: %v", p, err)
		}
	}

	got := rs.Keys()
	want := []string{"GET /a", "GET /m", "GET /z"}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Keys() = %v, want %v", got, want)
			break
		}
	}
}
