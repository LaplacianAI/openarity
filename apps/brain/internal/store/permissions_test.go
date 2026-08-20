package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Permissions and the routes that require them are data, so an enterprise can
// compose roles in a dashboard without a deploy. These tests hold the two
// things that keeps honest: a permission has to exist before it can be
// granted, and a route has to name a permission that exists.

// execErr runs a statement and returns whatever the database said, so a test
// can assert on the constraint rather than on a message.
func execErr(t *testing.T, s *Store, sql string, args ...any) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), sql, args...)
	return err
}

func pgCode(t *testing.T, err error) string {
	t.Helper()

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a Postgres error, got %v", err)
	}
	return pgErr.Code
}

// The hole this closes: role_permissions.action was plain text with nothing
// behind it, so "membership:writ" inserted happily, granted nothing, and said
// nothing. That is indistinguishable from a permissions bug at the far end.
func TestAGrantMustNameAPermissionThatExists(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s,
		`INSERT INTO role_permissions (role, action) VALUES ('admin', 'membership:writ')`)
	if err == nil {
		t.Fatal("granted a permission that does not exist")
	}
	if code := pgCode(t, err); code != foreignKeyViolation {
		t.Errorf("error code = %s, want %s (foreign key)", code, foreignKeyViolation)
	}
}

// Every action already granted must have a permission row, or the backfill in
// the migration missed one and the foreign key above is guarding nothing.
func TestEveryGrantedActionHasAPermission(t *testing.T) {
	s := queryStore(t)

	var orphans int
	err := s.pool.QueryRow(t.Context(), `
		SELECT count(*) FROM role_permissions rp
		WHERE NOT EXISTS (SELECT 1 FROM permissions p WHERE p.name = rp.action)`).Scan(&orphans)
	if err != nil {
		t.Fatalf("count orphaned grants: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d grants name a permission with no row", orphans)
	}
}

// A permission still granted to somebody must not vanish underneath them.
func TestAPermissionInUseCannotBeDeleted(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `DELETE FROM permissions WHERE name = 'membership:write'`)
	if err == nil {
		t.Fatal("deleted a permission that is still granted")
	}
	if code := pgCode(t, err); code != restrictViolation {
		t.Errorf("error code = %s, want %s (restrict)", code, restrictViolation)
	}
}

// A permission nobody holds is removable, and the loader depends on it: an
// entry dropped from rbac.json has to be able to go.
func TestAnUnusedPermissionCanBeDeleted(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO permissions (name, description) VALUES ('report:read', 'Read reports')`)
	if err := execErr(t, s, `DELETE FROM permissions WHERE name = 'report:read'`); err != nil {
		t.Errorf("delete an ungranted permission: %v", err)
	}
}

// The description is what a dashboard shows instead of "membership:write".
// Empty is allowed on the way in — the loader fills it — but the column must
// exist and must not be null.
func TestPermissionsCarryADescription(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s,
		`INSERT INTO permissions (name, description) VALUES ('report:write', NULL)`)
	if err == nil {
		t.Fatal("inserted a permission with a null description")
	}
	if code := pgCode(t, err); code != notNullViolation {
		t.Errorf("error code = %s, want %s (not null)", code, notNullViolation)
	}
}

// A route may require only a permission that exists, for the same reason a
// grant may: a typo would leave the route requiring something nobody can ever
// hold, which reads as "forbidden" for everyone.
func TestARouteMustNameAPermissionThatExists(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/nowhere', 'no:such', 'team')`)
	if err == nil {
		t.Fatal("a route required a permission that does not exist")
	}
	if code := pgCode(t, err); code != foreignKeyViolation {
		t.Errorf("error code = %s, want %s (foreign key)", code, foreignKeyViolation)
	}
}

// One route, one permission. Two rows for the same method and path would make
// which permission applies depend on row order.
func TestARouteIsMappedOnlyOnce(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/duplicates', 'membership:write', 'any_team')`)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/duplicates', 'agent:write', 'team')`)
	if err == nil {
		t.Fatal("the same method and path were mapped twice")
	}
	if code := pgCode(t, err); code != uniqueViolation {
		t.Errorf("error code = %s, want %s (unique)", code, uniqueViolation)
	}
}

// The same path under a different method is a different route.
func TestTheSamePathUnderTwoMethodsIsTwoRoutes(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/two-methods', 'membership:write', 'any_team')`)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('POST', '/two-methods', 'agent:write', 'team')`)
	if err != nil {
		t.Errorf("mapping POST alongside GET: %v", err)
	}
}

// scope decides which check runs. any_team is strictly weaker — an admin of
// one team passes it — so a value outside the two must not reach the loader as
// something to interpret.
func TestScopeIsConstrainedToTheTwoChecks(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/bad-scope', 'user:read', 'everywhere')`)
	if err == nil {
		t.Fatal("accepted a scope that maps to no check")
	}
	if code := pgCode(t, err); code != checkViolation {
		t.Errorf("error code = %s, want %s (check)", code, checkViolation)
	}
}

func TestEveryScopeIsAccepted(t *testing.T) {
	s := queryStore(t)

	for _, scope := range []string{"team", "any_team"} {
		err := execErr(t, s, `
			INSERT INTO route_permissions (method, path, permission, scope)
			VALUES ('GET', $1, 'membership:write', $2)`, "/scope-"+scope, scope)
		if err != nil {
			t.Errorf("scope %q rejected: %v", scope, err)
		}
	}

	// These two need no permission — the check is membership, or the super
	// admin list in configuration.
	for _, scope := range []string{"authenticated", "member", "super_admin"} {
		err := execErr(t, s, `
			INSERT INTO route_permissions (method, path, permission, scope)
			VALUES ('GET', $1, NULL, $2)`, "/scope-"+scope, scope)
		if err != nil {
			t.Errorf("scope %q rejected: %v", scope, err)
		}
	}
}

// A team-scoped route with no permission would check nothing at all — the
// worst shape available, because the row exists so the startup check passes.
func TestATeamScopedRouteMustNameAPermission(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('GET', '/no-permission', NULL, 'team')`)
	if err == nil {
		t.Fatal("a team-scoped route was mapped with no permission")
	}
	if code := pgCode(t, err); code != checkViolation {
		t.Errorf("error code = %s, want %s (check)", code, checkViolation)
	}
}

// And the reverse: naming one where it is not consulted implies it matters.
func TestASuperAdminRouteMustNotNameAPermission(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('POST', '/super-with-permission', 'membership:write', 'super_admin')`)
	if err == nil {
		t.Fatal("a super-admin route named a permission that is never checked")
	}
	if code := pgCode(t, err); code != checkViolation {
		t.Errorf("error code = %s, want %s (check)", code, checkViolation)
	}
}

// The method is half the key, so a lower-case one would map a route the mux
// never serves — http.ServeMux patterns are upper case.
func TestMethodMustBeUpperCase(t *testing.T) {
	s := queryStore(t)

	err := execErr(t, s, `
		INSERT INTO route_permissions (method, path, permission, scope)
		VALUES ('get', '/lower-method', 'membership:write', 'any_team')`)
	if err == nil {
		t.Fatal("accepted a lower-case method")
	}
	if code := pgCode(t, err); code != checkViolation {
		t.Errorf("error code = %s, want %s (check)", code, checkViolation)
	}
}
