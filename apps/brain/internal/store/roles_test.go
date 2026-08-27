package store

import (
	"slices"
	"testing"
)

// Roles and the permissions they hold are both data an admin can edit. These
// tests hold what the database still has to guarantee: the shipped roles are
// right, a role in use cannot vanish, and nothing may grant a role that does
// not exist.
//
// Whether the grants match rbac.json is rbac_test.go's question, not this
// file's.

// The two seeded roles are the system working at all. A deployment that boots
// with an empty roles table can grant nobody anything, and the failure is
// silent — every membership insert simply fails a foreign key.
func TestSeededRolesExist(t *testing.T) {
	s := queryStore(t)

	roles, err := s.ListRoles(t.Context())
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}

	var names []string
	for _, r := range roles {
		names = append(names, r.Name)
		if r.Description == "" {
			t.Errorf("role %q has no description", r.Name)
		}
	}
	if !slices.Equal(names, []string{"admin", "member"}) {
		t.Errorf("roles = %v, want [admin member]", names)
	}
}

func TestSeededPermissions(t *testing.T) {
	s := queryStore(t)

	for role, want := range map[string][]string{
		"admin": {
			"agent:write", "channel:write", "membership:write",
			"session:read_all", "tool:write", "user:read",
		},
		"member": {"agent:write", "tool:write"},
	} {
		got, err := s.ListRolePermissions(t.Context(), role)
		if err != nil {
			t.Fatalf("ListRolePermissions(%s): %v", role, err)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s permissions = %v, want %v", role, got, want)
		}
	}
}

// admin must be able to do everything member can. A member who can do
// something their admin cannot is a hole nobody would think to look for.
func TestAdminIsASupersetOfMember(t *testing.T) {
	s := queryStore(t)

	admin, err := s.ListRolePermissions(t.Context(), "admin")
	if err != nil {
		t.Fatalf("ListRolePermissions(admin): %v", err)
	}
	member, err := s.ListRolePermissions(t.Context(), "member")
	if err != nil {
		t.Fatalf("ListRolePermissions(member): %v", err)
	}

	for _, action := range member {
		if !slices.Contains(admin, action) {
			t.Errorf("member may %q and admin may not", action)
		}
	}
}

// A role nobody holds should be removable. Its permissions go with it —
// leaving them behind means recreating the role silently restores old grants.
func TestDeletingAnUnusedRoleTakesItsPermissions(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO roles (name) VALUES ('auditor')`)
	exec(t, s, `INSERT INTO role_permissions (role, action) VALUES ('auditor', 'agent:write')`)

	exec(t, s, `DELETE FROM roles WHERE name = 'auditor'`)

	perms, err := s.ListRolePermissions(t.Context(), "auditor")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	if len(perms) != 0 {
		t.Errorf("%v survived the role", perms)
	}
}

// Deleting a role people hold must fail rather than silently strip their
// memberships. An admin removing "member" should be told twelve people
// are using it, not discover it from a support ticket.
func TestARoleInUseCannotBeDeleted(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "member")

	_, err := s.pool.Exec(t.Context(), `DELETE FROM roles WHERE name = 'member'`)
	wantPGCode(t, err, foreignKeyViolation, "deleting a role somebody holds")

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 1 || rows[0].Role != "member" {
		t.Errorf("the membership was disturbed: %+v", rows)
	}
}

// The CHECK constraint became a foreign key. It still has to constrain, and
// now it constrains against a table rather than a literal list.
func TestTeamMembersRejectARoleThatDoesNotExist(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)

	for _, role := range []string{"owner", "Admin", "ADMIN", "", "superuser"} {
		err := insertMember(t, s, team.ID, user.ID, role)
		wantPGCode(t, err, foreignKeyViolation, "role "+role)
	}
}

// A role invented by an admin is immediately grantable — that is the whole
// point of moving roles into a table.
func TestANewRoleCanBeGrantedWithoutADeploy(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO roles (name, description) VALUES ('release-manager', 'Ships things')`)
	exec(t, s, `INSERT INTO role_permissions (role, action) VALUES ('release-manager', 'agent:write')`)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "release-manager")

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 1 || rows[0].Role != "release-manager" {
		t.Errorf("rows = %+v, want the new role", rows)
	}
}

// Permissions cannot be granted to a role that does not exist. Otherwise a
// typo produces rules that apply to nobody and are never noticed.
func TestPermissionsRequireAnExistingRole(t *testing.T) {
	s := queryStore(t)

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO role_permissions (role, action) VALUES ('ghost', 'agent:write')`)
	wantPGCode(t, err, foreignKeyViolation, "a permission for a role that does not exist")
}

// The composite key makes granting the same action twice a no-op rather than
// two rows that have to agree.
func TestAPermissionCannotBeGrantedTwice(t *testing.T) {
	s := queryStore(t)

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO role_permissions (role, action) VALUES ('admin', 'agent:write')`)
	wantPGCode(t, err, uniqueViolation, "granting the same action to the same role twice")
}

// A role with no permissions is legal — an admin creates it, then grants. It
// must come back as an empty list rather than an error.
func TestARoleWithNoPermissionsIsEmptyNotAnError(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO roles (name) VALUES ('newcomer')`)

	perms, err := s.ListRolePermissions(t.Context(), "newcomer")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	if len(perms) != 0 {
		t.Errorf("a fresh role already grants %v", perms)
	}
}

func TestListRolePermissionsIsScopedToItsRole(t *testing.T) {
	s := queryStore(t)

	perms, err := s.ListRolePermissions(t.Context(), "member")
	if err != nil {
		t.Fatalf("ListRolePermissions: %v", err)
	}
	if slices.Contains(perms, "membership:write") {
		t.Errorf("member holds an admin-only action: %v", perms)
	}
}

func TestListRolesIsOrderedByName(t *testing.T) {
	s := queryStore(t)

	exec(t, s, `INSERT INTO roles (name) VALUES ('zebra'), ('aardvark')`)

	roles, err := s.ListRoles(t.Context())
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}

	var names []string
	for _, r := range roles {
		names = append(names, r.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("roles are not ordered by name: %v", names)
	}
}

// Down has to restore the CHECK it replaced, not just drop the tables — a
// rollback that leaves team_members.role unconstrained accepts anything.
func TestRolesRollBackRestoresTheCheck(t *testing.T) {
	s := queryStore(t)

	// Roll back until the roles migration itself is undone rather than
	// assuming it is the newest. Every migration added after it would
	// otherwise break this test for a reason that has nothing to do with
	// roles.
	//
	// The bound is the number of migrations that exist rather than a number
	// somebody picked. It was 10, and the eleventh migration after roles made
	// this fail with three assertions about roles surviving a rollback that
	// had simply run out of turns.
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}

	rolledBack := 0
	for tableExists(t, s, "roles") {
		if rolledBack == len(files) {
			t.Fatalf("rolled back %d times and roles still exists, which is every "+
				"migration there is", rolledBack)
		}
		if err := s.Rollback(t.Context()); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		rolledBack++
	}

	for _, table := range []string{"roles", "role_permissions"} {
		if tableExists(t, s, table) {
			t.Errorf("%s survived the rollback", table)
		}
	}

	var def string
	err = s.pool.QueryRow(t.Context(), `
		SELECT pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = 'team_members'
		  AND n.nspname = current_schema()
		  AND c.contype = 'c'`).Scan(&def)
	if err != nil {
		t.Fatalf("the CHECK constraint was not restored: %v", err)
	}
}

// ListRoles and ListRolePermissions decide what a caller may do, so a partial
// answer is a partial set of permissions. Both have the same three failure
// branches and none may return the rows it managed to read.

func TestRoleQueriesReportAQueryFailure(t *testing.T) {
	s := queryStore(t)
	s.Close()

	if roles, err := s.ListRoles(t.Context()); err == nil {
		t.Errorf("ListRoles succeeded against a closed pool: %v", roles)
	} else if roles != nil {
		t.Errorf("ListRoles returned %v alongside the error", roles)
	}

	if perms, err := s.ListRolePermissions(t.Context(), "admin"); err == nil {
		t.Errorf("ListRolePermissions succeeded against a closed pool: %v", perms)
	} else if perms != nil {
		t.Errorf("ListRolePermissions returned %v alongside the error", perms)
	}
}

// A row arrives and will not scan. Both queries read into string, and pgx
// converts far more than it looks like it will — an integer column scans into
// a string without complaint, and so does jsonb. An array does not, which is
// what makes it usable here.
func TestRoleQueriesReportAScanFailure(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "ALTER TABLE team_members DROP CONSTRAINT team_members_role_fkey")
	exec(t, s, "ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_role_fkey")
	exec(t, s, "DROP TABLE roles")
	exec(t, s, `CREATE VIEW roles AS SELECT ARRAY['a'] AS name, '' AS description`)
	exec(t, s, "DROP TABLE role_permissions")
	exec(t, s, `CREATE VIEW role_permissions AS SELECT 'admin'::text AS role, ARRAY['a'] AS action`)

	if roles, err := s.ListRoles(t.Context()); err == nil {
		t.Errorf("ListRoles accepted a row it cannot scan: %v", roles)
	} else if roles != nil {
		t.Errorf("ListRoles returned %v alongside the error", roles)
	}

	if perms, err := s.ListRolePermissions(t.Context(), "admin"); err == nil {
		t.Errorf("ListRolePermissions accepted a row it cannot scan: %v", perms)
	} else if perms != nil {
		t.Errorf("ListRolePermissions returned %v alongside the error", perms)
	}
}

// Rows start arriving and then the server raises. The failure lands after 499
// good rows, so it surfaces from rows.Err() rather than from Query — the
// branch that would otherwise return a partial permission set as a success.
func TestRoleQueriesReportAFailureMidStream(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "ALTER TABLE team_members DROP CONSTRAINT team_members_role_fkey")
	exec(t, s, "ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_role_fkey")
	exec(t, s, "DROP TABLE roles")
	exec(t, s, `CREATE VIEW roles AS
		SELECT CASE WHEN i < 500 THEN 'ok' ELSE (1/(500-i))::text END AS name,
		       '' AS description
		FROM generate_series(1, 1000) i`)
	exec(t, s, "DROP TABLE role_permissions")
	exec(t, s, `CREATE VIEW role_permissions AS
		SELECT 'admin'::text AS role,
		       CASE WHEN i < 500 THEN 'ok' ELSE (1/(500-i))::text END AS action
		FROM generate_series(1, 1000) i`)

	if roles, err := s.ListRoles(t.Context()); err == nil {
		t.Errorf("ListRoles returned a partial list as a success: %d rows", len(roles))
	} else if roles != nil {
		t.Errorf("ListRoles returned %d rows alongside the error", len(roles))
	}

	if perms, err := s.ListRolePermissions(t.Context(), "admin"); err == nil {
		t.Errorf("ListRolePermissions returned a partial list as a success: %d rows", len(perms))
	} else if perms != nil {
		t.Errorf("ListRolePermissions returned %d rows alongside the error", len(perms))
	}
}
