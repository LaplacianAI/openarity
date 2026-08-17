package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// The schema is the last line of defence. Every constraint here is one an
// application bug can reach, and the point of asserting them is that they hold
// when the Go code that was supposed to prevent it does not.

// Postgres error codes, from
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	uniqueViolation     = "23505"
	checkViolation      = "23514"
	foreignKeyViolation = "23503"
	notNullViolation    = "23502"
)

// insertUser writes a user directly, bypassing any query layer, and returns
// its id.
func insertUser(t *testing.T, s *Store, issuer, subject string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := s.pool.QueryRow(t.Context(),
		`INSERT INTO users (issuer, subject, email) VALUES ($1, $2, $3) RETURNING id`,
		issuer, subject, subject+"@example.com").Scan(&id)
	if err != nil {
		t.Fatalf("insert user (%s, %s): %v", issuer, subject, err)
	}
	return id
}

func insertMember(t *testing.T, s *Store, teamID, userID uuid.UUID, role string) error {
	t.Helper()

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`,
		teamID, userID, role)
	return err
}

// wantPGCode fails unless the error is the named Postgres constraint
// violation. Asserting the code rather than the message means a Postgres
// upgrade that rewords the text does not fail the suite.
func wantPGCode(t *testing.T, err error, code, what string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s: the database accepted it", what)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: error is not a PgError: %v", what, err)
	}
	if pgErr.Code != code {
		t.Errorf("%s: SQLSTATE %s (%s), want %s", what, pgErr.Code, pgErr.Message, code)
	}
}

// The same person cannot be registered twice. This is also what the
// first-login upsert targets with ON CONFLICT — without the constraint that
// statement will not even parse.
func TestUsersRejectADuplicateIssuerAndSubject(t *testing.T) {
	s := queryStore(t)

	insertUser(t, s, "https://idp.example.com", "user-42")

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ($1, $2)`,
		"https://idp.example.com", "user-42")
	wantPGCode(t, err, uniqueViolation, "the same (issuer, subject) twice")
}

// sub is only unique within an issuer. Two providers both numbering their
// users from 1 must not collide — this is the reason the key is the pair.
func TestUsersAllowTheSameSubjectUnderADifferentIssuer(t *testing.T) {
	s := queryStore(t)

	insertUser(t, s, "https://idp-a.example.com", "1")
	insertUser(t, s, "https://idp-b.example.com", "1")
}

// Email is optional: a token need not carry the claim, and its absence is not
// an authentication failure.
func TestUsersAcceptAMissingEmail(t *testing.T) {
	s := queryStore(t)

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO users (issuer, subject) VALUES ('https://idp', 'no-email')`)
	if err != nil {
		t.Fatalf("insert without an email: %v", err)
	}
}

// Issuer and subject are the identity. A row missing either identifies nobody
// and would match every lookup that passes an empty string.
func TestUsersRequireBothHalvesOfTheIdentity(t *testing.T) {
	s := queryStore(t)

	for _, tc := range []struct{ name, sql string }{
		{"no issuer", `INSERT INTO users (subject) VALUES ('s')`},
		{"no subject", `INSERT INTO users (issuer) VALUES ('https://idp')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), tc.sql)
			wantPGCode(t, err, notNullViolation, tc.name)
		})
	}
}

func TestTeamMembersAcceptTheDefinedRoles(t *testing.T) {
	s := queryStore(t)

	user := insertUser(t, s, "https://idp", "user-1")
	for _, role := range []string{"admin", "member"} {
		team := mustCreate(t, s, "team-"+role)
		if err := insertMember(t, s, team.ID, user, role); err != nil {
			t.Errorf("role %q was rejected: %v", role, err)
		}
	}
}

// One role per user per team, enforced by the composite primary key rather
// than by an application check. Two rows would make "what can this user do
// here" ambiguous and order-dependent.
func TestTeamMembersRejectASecondRoleForTheSamePair(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := insertUser(t, s, "https://idp", "user-1")

	if err := insertMember(t, s, team.ID, user, "member"); err != nil {
		t.Fatalf("first membership: %v", err)
	}

	err := insertMember(t, s, team.ID, user, "admin")
	wantPGCode(t, err, uniqueViolation, "a second role for the same (team, user)")
}

// A membership pointing at a team or user that does not exist is a grant to
// nobody, and it survives every join that would otherwise reveal it.
func TestTeamMembersRejectDanglingReferences(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := insertUser(t, s, "https://idp", "user-1")

	t.Run("unknown team", func(t *testing.T) {
		wantPGCode(t, insertMember(t, s, uuid.New(), user, "admin"),
			foreignKeyViolation, "membership in a team that does not exist")
	})
	t.Run("unknown user", func(t *testing.T) {
		wantPGCode(t, insertMember(t, s, team.ID, uuid.New(), "admin"),
			foreignKeyViolation, "membership for a user that does not exist")
	})
}

// Deleting a team must take its memberships with it. A leftover row is a grant
// on a team that no longer exists, and it comes back the moment an id is
// reused.
func TestDeletingATeamRemovesItsMemberships(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := insertUser(t, s, "https://idp", "user-1")
	if err := insertMember(t, s, team.ID, user, "admin"); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	if err := s.DeleteTeam(t.Context(), team.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if n := countMemberships(t, s); n != 0 {
		t.Errorf("%d memberships survived the team", n)
	}
}

// Deleting a user must take their memberships too, and must not take the team.
func TestDeletingAUserRemovesTheirMembershipsButNotTheTeam(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := insertUser(t, s, "https://idp", "user-1")
	if err := insertMember(t, s, team.ID, user, "admin"); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if n := countMemberships(t, s); n != 0 {
		t.Errorf("%d memberships survived the user", n)
	}
	if _, err := s.GetTeam(t.Context(), team.ID); err != nil {
		t.Errorf("the team was removed along with its member: %v", err)
	}
}

// "Which teams is this user in" runs on every authenticated request. The
// composite primary key indexes (team_id, user_id), which is the wrong order
// for that lookup, so a separate index on user_id has to exist.
func TestTeamMembersAreIndexedByUser(t *testing.T) {
	s := queryStore(t)

	// Restricted to this test's schema. pg_class.relname matches every schema
	// in the database, and every other test in this package has a
	// team_members of its own — without the namespace filter this finds
	// somebody else's index and passes no matter what the migration says.
	var indexed bool
	err := s.pool.QueryRow(t.Context(), `
		SELECT EXISTS (
		    SELECT 1 FROM pg_index i
		    JOIN pg_class c ON c.oid = i.indrelid
		    JOIN pg_namespace n ON n.oid = c.relnamespace
		    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = i.indkey[0]
		    WHERE c.relname = 'team_members'
		      AND n.nspname = current_schema()
		      AND a.attname = 'user_id'
		)`).Scan(&indexed)
	if err != nil {
		t.Fatalf("inspect indexes: %v", err)
	}
	if !indexed {
		t.Error("no index leads with team_members.user_id — every request scans the table")
	}
}

// Down must undo Up completely, and no further. A migration whose Down reaches
// into an earlier migration's tables makes the stack impossible to unwind one
// step at a time.
//
// Rollback undoes one migration and this is no longer the newest, so step down
// until users goes rather than hard-coding a count that breaks on the next
// migration added above it.
func TestUsersAndTeamMembersRollBackCleanly(t *testing.T) {
	s := migrationStore(t)

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const maxSteps = 100
	for step := 0; tableExists(t, s, "users"); step++ {
		if step == maxSteps {
			t.Fatalf("users still there after %d rollbacks", maxSteps)
		}
		if err := s.Rollback(t.Context()); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
	}

	if tableExists(t, s, "team_members") {
		t.Error("team_members survived its own migration's rollback")
	}
	if !tableExists(t, s, "teams") {
		t.Error("rolling back to the users migration removed an earlier migration's table")
	}
}

func countMemberships(t *testing.T, s *Store) int {
	t.Helper()

	var n int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM team_members`).Scan(&n); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	return n
}
