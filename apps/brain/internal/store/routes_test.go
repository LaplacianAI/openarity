package store

import (
	"testing"
)

// The brain reads this list once at startup and guards every request with it.
// A partial answer is a partial set of guards, so these tests are as much
// about the three failure paths as about the happy one.

func TestListRoutePermissionsReturnsEveryMappedRoute(t *testing.T) {
	s := queryStore(t)

	routes, err := s.ListRoutePermissions(t.Context())
	if err != nil {
		t.Fatalf("ListRoutePermissions: %v", err)
	}

	f, err := parseRBAC(rbacJSON)
	if err != nil {
		t.Fatalf("rbac.json: %v", err)
	}
	if len(routes) != len(f.Routes) {
		t.Errorf("got %d routes, rbac.json maps %d", len(routes), len(f.Routes))
	}
}

func TestARouteCarriesItsScopeAndPermission(t *testing.T) {
	s := queryStore(t)

	routes, err := s.ListRoutePermissions(t.Context())
	if err != nil {
		t.Fatalf("ListRoutePermissions: %v", err)
	}

	for _, r := range routes {
		if r.Method != "GET" || r.Path != "/users" {
			continue
		}
		if r.Scope != "any_team" {
			t.Errorf("GET /users scope = %q, want any_team", r.Scope)
		}
		if r.Permission == nil {
			t.Fatal("GET /users requires no permission")
		}
		if *r.Permission != "user:read" {
			t.Errorf("GET /users permission = %q, want user:read", *r.Permission)
		}
		return
	}
	t.Error("GET /users is not in the list")
}

// Null rather than an empty string, so "requires nothing" cannot be confused
// with "requires a permission whose name is empty" — which would be a
// permission nobody can ever hold, and so a route nobody can ever reach.
func TestARouteThatNeedsNoPermissionComesBackNil(t *testing.T) {
	s := queryStore(t)

	routes, err := s.ListRoutePermissions(t.Context())
	if err != nil {
		t.Fatalf("ListRoutePermissions: %v", err)
	}

	for _, r := range routes {
		if r.Method == "GET" && r.Path == "/whoami" {
			if r.Permission != nil {
				t.Errorf("GET /whoami carries permission %q", *r.Permission)
			}
			return
		}
	}
	t.Error("GET /whoami is not in the list")
}

// The startup check compares this against the mux, and reports what is
// missing. Unordered rows would make that report shuffle between boots for no
// reason, which is enough to make an operator doubt it.
func TestRoutesComeBackOrdered(t *testing.T) {
	s := queryStore(t)

	routes, err := s.ListRoutePermissions(t.Context())
	if err != nil {
		t.Fatalf("ListRoutePermissions: %v", err)
	}

	var last string
	for _, r := range routes {
		key := r.Path + " " + r.Method
		if key < last {
			t.Errorf("%q came after %q", key, last)
		}
		last = key
	}
}

// Every failure path returns no rows alongside the error. A caller that
// ignored the error and guarded nothing is the shape this prevents.

func TestListRoutePermissionsReportsAQueryFailure(t *testing.T) {
	s := queryStore(t)
	s.Close()

	routes, err := s.ListRoutePermissions(t.Context())
	if err == nil {
		t.Errorf("succeeded against a closed pool: %v", routes)
	} else if routes != nil {
		t.Errorf("returned %v alongside the error", routes)
	}
}

// A row arrives and will not scan. pgx converts more than it looks like it
// will — an integer scans into a string without complaint — so the column is
// replaced with an array, which does not.
func TestListRoutePermissionsReportsAScanFailure(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "DROP TABLE route_permissions")
	exec(t, s, `CREATE VIEW route_permissions AS
		SELECT ARRAY['a'] AS method, ''::text AS path,
		       NULL::text AS permission, ''::text AS scope`)

	routes, err := s.ListRoutePermissions(t.Context())
	if err == nil {
		t.Errorf("accepted a row it cannot scan: %v", routes)
	} else if routes != nil {
		t.Errorf("returned %v alongside the error", routes)
	}
}

// Rows start arriving and then the server raises. The failure lands after 499
// good rows, so it surfaces from rows.Err() rather than from Query — the
// branch that would otherwise return a partial route table as a success, and
// leave every route past the 499th unguarded.
func TestListRoutePermissionsReportsAFailureMidStream(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "DROP TABLE route_permissions")
	exec(t, s, `CREATE VIEW route_permissions AS
		SELECT 'GET'::text AS method,
		       CASE WHEN i < 500 THEN '/ok' ELSE (1/(500-i))::text END AS path,
		       NULL::text AS permission,
		       'authenticated'::text AS scope
		FROM generate_series(1, 1000) i`)

	routes, err := s.ListRoutePermissions(t.Context())
	if err == nil {
		t.Errorf("returned a partial route table as a success: %d rows", len(routes))
	} else if routes != nil {
		t.Errorf("returned %d rows alongside the error", len(routes))
	}
}
