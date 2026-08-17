package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const testIssuer = "https://idp.example.com"

func ptr(s string) *string { return &s }

// upsert is the first-login provisioning path, called on every authenticated
// request.
func upsert(t *testing.T, s *Store, issuer, subject string, email *string) db.User {
	t.Helper()

	user, err := s.UpsertUser(t.Context(), db.UpsertUserParams{
		Issuer: issuer, Subject: subject, Email: email,
	})
	if err != nil {
		t.Fatalf("UpsertUser(%s, %s): %v", issuer, subject, err)
	}
	return user
}

func addMember(t *testing.T, s *Store, teamID, userID uuid.UUID, role string) {
	t.Helper()

	if _, err := s.AddTeamMember(t.Context(), db.AddTeamMemberParams{
		TeamID: teamID, UserID: userID, Role: role,
	}); err != nil {
		t.Fatalf("AddTeamMember: %v", err)
	}
}

func teamNamesOf(rows []db.ListUserTeamsRow) []string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names
}

func TestUpsertUserCreatesOnFirstLogin(t *testing.T) {
	s := queryStore(t)

	user := upsert(t, s, testIssuer, "user-42", ptr("someone@example.com"))

	if user.ID == uuid.Nil {
		t.Error("no id was assigned")
	}
	if user.Issuer != testIssuer || user.Subject != "user-42" {
		t.Errorf("identity = (%q, %q), want (%q, user-42)", user.Issuer, user.Subject, testIssuer)
	}
	if user.Email == nil || *user.Email != "someone@example.com" {
		t.Errorf("Email = %v, want someone@example.com", user.Email)
	}
}

// The second login must return the same row, not a new one, and not an error.
// This runs on every request, so a conflict has to be the normal path.
func TestUpsertUserIsStableAcrossLogins(t *testing.T) {
	s := queryStore(t)

	first := upsert(t, s, testIssuer, "user-42", ptr("a@example.com"))
	second := upsert(t, s, testIssuer, "user-42", ptr("a@example.com"))

	if first.ID != second.ID {
		t.Errorf("a second login produced a new id: %s then %s", first.ID, second.ID)
	}
	if n := countUsers(t, s); n != 1 {
		t.Errorf("%d user rows after two logins, want 1", n)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at moved: %v then %v", first.CreatedAt, second.CreatedAt)
	}
}

// A DO NOTHING conflict clause returns no row at all, so every returning user
// would come back as pgx.ErrNoRows. This is the assertion that catches that.
func TestUpsertUserReturnsARowOnConflict(t *testing.T) {
	s := queryStore(t)

	upsert(t, s, testIssuer, "user-42", nil)

	user, err := s.UpsertUser(t.Context(), db.UpsertUserParams{
		Issuer: testIssuer, Subject: "user-42",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("the conflict path returned no row — DO UPDATE is what makes it return one")
	}
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if user.Subject != "user-42" {
		t.Errorf("Subject = %q, want user-42", user.Subject)
	}
}

// Changing an email at the identity provider is the only way it ever reaches
// this database. If the conflict clause does not update, the row keeps a stale
// address forever.
func TestUpsertUserUpdatesAChangedEmail(t *testing.T) {
	s := queryStore(t)

	upsert(t, s, testIssuer, "user-42", ptr("old@example.com"))
	updated := upsert(t, s, testIssuer, "user-42", ptr("new@example.com"))

	if updated.Email == nil || *updated.Email != "new@example.com" {
		t.Errorf("Email = %v, want new@example.com", updated.Email)
	}
}

// The email claim is optional, so it can appear and disappear between logins.
// Both directions have to work.
func TestUpsertUserHandlesAnEmailComingAndGoing(t *testing.T) {
	s := queryStore(t)

	if user := upsert(t, s, testIssuer, "user-42", nil); user.Email != nil {
		t.Errorf("Email = %v on first login without the claim, want nil", user.Email)
	}
	if user := upsert(t, s, testIssuer, "user-42", ptr("a@example.com")); user.Email == nil {
		t.Error("Email stayed nil after the claim appeared")
	}
	if user := upsert(t, s, testIssuer, "user-42", nil); user.Email != nil {
		t.Errorf("Email = %v after the claim disappeared, want nil", user.Email)
	}
}

// Postgres does not touch updated_at on its own. Without SET updated_at =
// now() the column freezes at insert time and quietly starts lying.
func TestUpsertUserAdvancesUpdatedAt(t *testing.T) {
	s := queryStore(t)

	first := upsert(t, s, testIssuer, "user-42", ptr("a@example.com"))

	// Move it back so the second upsert has something to advance past,
	// regardless of clock resolution.
	if _, err := s.pool.Exec(t.Context(),
		"UPDATE users SET updated_at = $1 WHERE id = $2",
		time.Now().Add(-time.Hour), first.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	second := upsert(t, s, testIssuer, "user-42", ptr("a@example.com"))
	if !second.UpdatedAt.After(first.CreatedAt.Add(-time.Minute)) {
		t.Errorf("updated_at = %v, it did not advance", second.UpdatedAt)
	}
}

// Two identity providers numbering their users from the same strings are two
// different people. This is why the key is the pair.
func TestUpsertUserKeepsIssuersApart(t *testing.T) {
	s := queryStore(t)

	a := upsert(t, s, "https://idp-a.example.com", "1", nil)
	b := upsert(t, s, "https://idp-b.example.com", "1", nil)

	if a.ID == b.ID {
		t.Error("the same subject under two issuers resolved to one user")
	}
	if n := countUsers(t, s); n != 2 {
		t.Errorf("%d user rows, want 2", n)
	}
}

func TestGetUserReturnsTheUpsertedRow(t *testing.T) {
	s := queryStore(t)

	created := upsert(t, s, testIssuer, "user-42", ptr("a@example.com"))

	got, err := s.GetUser(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.ID != created.ID || got.Subject != created.Subject {
		t.Errorf("GetUser returned %+v, want %+v", got, created)
	}
}

func TestGetUserReportsAnUnknownID(t *testing.T) {
	s := queryStore(t)

	if _, err := s.GetUser(t.Context(), uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetUser on an unknown id = %v, want pgx.ErrNoRows", err)
	}
}

// A freshly registered user is in no teams. Nothing has gone wrong — that is
// how "registered, awaiting access" is represented, so it must be an empty
// list rather than an error.
func TestListUserTeamsIsEmptyForANewUser(t *testing.T) {
	s := queryStore(t)

	user := upsert(t, s, testIssuer, "user-42", nil)

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a new user is in %d teams: %v", len(rows), teamNamesOf(rows))
	}
}

func TestListUserTeamsReturnsTheRoleWithTheTeam(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "admin")

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %v", len(rows), teamNamesOf(rows))
	}
	if rows[0].ID != team.ID || rows[0].Name != "platform" || rows[0].Role != "admin" {
		t.Errorf("row = %+v, want the platform team at role admin", rows[0])
	}
}

// Scoping is the whole point. A query returning another user's memberships
// would grant their access on every request.
func TestListUserTeamsSeesOnlyItsOwnUser(t *testing.T) {
	s := queryStore(t)

	mine := mustCreate(t, s, "mine")
	theirs := mustCreate(t, s, "theirs")

	me := upsert(t, s, testIssuer, "me", nil)
	them := upsert(t, s, testIssuer, "them", nil)
	addMember(t, s, mine.ID, me.ID, "member")
	addMember(t, s, theirs.ID, them.ID, "admin")

	rows, err := s.ListUserTeams(t.Context(), me.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if got := teamNamesOf(rows); len(got) != 1 || got[0] != "mine" {
		t.Errorf("saw %v, want only [mine]", got)
	}
}

// Unordered results make whoami's output flicker between identical calls.
func TestListUserTeamsIsOrderedByName(t *testing.T) {
	s := queryStore(t)

	user := upsert(t, s, testIssuer, "user-42", nil)
	for _, name := range []string{"zulu", "alpha", "mike"} {
		team := mustCreate(t, s, name)
		addMember(t, s, team.ID, user.ID, "member")
	}

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}

	want := []string{"alpha", "mike", "zulu"}
	got := teamNamesOf(rows)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A user in several teams holds a different role in each. "This user is an
// admin" is meaningless without naming the team.
func TestListUserTeamsCarriesADifferentRolePerTeam(t *testing.T) {
	s := queryStore(t)

	user := upsert(t, s, testIssuer, "user-42", nil)
	alpha := mustCreate(t, s, "alpha")
	bravo := mustCreate(t, s, "bravo")
	addMember(t, s, alpha.ID, user.ID, "admin")
	addMember(t, s, bravo.ID, user.ID, "member")

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}

	roles := map[string]string{}
	for _, r := range rows {
		roles[r.Name] = r.Role
	}
	if roles["alpha"] != "admin" || roles["bravo"] != "member" {
		t.Errorf("roles = %v, want alpha:admin bravo:member", roles)
	}
}

// Granting twice must fail rather than silently produce two roles. An admin
// re-running a command is not the same as a role change.
func TestAddTeamMemberRefusesADuplicate(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "member")

	_, err := s.AddTeamMember(t.Context(), db.AddTeamMemberParams{
		TeamID: team.ID, UserID: user.ID, Role: "admin",
	})
	wantPGCode(t, err, uniqueViolation, "granting the same user a second role")
}

func TestRemoveTeamMemberRevokesAccess(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "admin")

	if err := s.RemoveTeamMember(t.Context(), db.RemoveTeamMemberParams{
		TeamID: team.ID, UserID: user.ID,
	}); err != nil {
		t.Fatalf("RemoveTeamMember: %v", err)
	}

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("access survived removal: %v", teamNamesOf(rows))
	}
}

// Revoking a membership that is not there is not an error — an admin removing
// somebody twice should not see a failure. But it must not remove anything
// else either.
func TestRemoveTeamMemberIsIdempotentAndScoped(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	keep := upsert(t, s, testIssuer, "keep", nil)
	gone := upsert(t, s, testIssuer, "gone", nil)
	addMember(t, s, team.ID, keep.ID, "admin")

	for range 2 {
		if err := s.RemoveTeamMember(t.Context(), db.RemoveTeamMemberParams{
			TeamID: team.ID, UserID: gone.ID,
		}); err != nil {
			t.Fatalf("removing an absent membership: %v", err)
		}
	}

	rows, err := s.ListUserTeams(t.Context(), keep.ID)
	if err != nil {
		t.Fatalf("ListUserTeams: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("removing one user's membership affected another: %v", teamNamesOf(rows))
	}
}

func countUsers(t *testing.T, s *Store) int {
	t.Helper()

	var n int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	return n
}

// ListUserTeams decides what a caller may do, so a partial list is a partial
// set of permissions. Each of its three failure branches must return an error
// rather than the rows it managed to read.

// Branch one: the query never runs.
func TestListUserTeamsReportsAQueryFailure(t *testing.T) {
	s := queryStore(t)
	user := upsert(t, s, testIssuer, "user-42", nil)
	s.Close()

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err == nil {
		t.Fatal("ListUserTeams succeeded against a closed pool")
	}
	if rows != nil {
		t.Errorf("returned %v alongside the error, want nil", teamNamesOf(rows))
	}
}

// Branch two: a row arrives and will not scan. Replacing teams with a view
// whose id is text makes the generated Scan fail on the first row.
func TestListUserTeamsReportsAScanFailure(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)
	addMember(t, s, team.ID, user.ID, "admin")

	// The schema is this test's own and is dropped afterwards, so mangling it
	// is free.
	//
	// Two things have to be true at once, and each was got wrong first. The
	// query must still plan, so both sides of `t.id = tm.team_id` are widened
	// to text — leaving team_members alone makes it a `text = uuid` planning
	// error, which returns from Query and never reaches Scan. And the value
	// must not be a uuid: pgx happily parses a valid uuid out of a text
	// column, so the scan would succeed.
	exec(t, s, "ALTER TABLE team_members DROP CONSTRAINT team_members_team_id_fkey")
	exec(t, s, "ALTER TABLE team_members ALTER COLUMN team_id TYPE text")
	exec(t, s, "UPDATE team_members SET team_id = 'not-a-uuid'")
	exec(t, s, "ALTER TABLE teams RENAME TO teams_real")
	exec(t, s, `CREATE VIEW teams AS
		SELECT 'not-a-uuid'::text AS id, name, created_at, updated_at FROM teams_real`)

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err == nil {
		t.Fatal("ListUserTeams accepted a row it cannot scan")
	}
	if rows != nil {
		t.Errorf("returned %v alongside the error, want nil", teamNamesOf(rows))
	}
}

// Branch three: rows start arriving and then the server raises. A view over
// generate_series divides by zero at row 500, so the failure lands after 499
// rows have already been produced and surfaces from rows.Err() rather than
// from Query.
//
// This is the branch that would otherwise hand a caller a partial list — and
// for this query a partial list of teams is a partial list of permissions.
func TestListUserTeamsReportsAFailureMidStream(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	user := upsert(t, s, testIssuer, "user-42", nil)

	exec(t, s, "DROP TABLE team_members")
	exec(t, s, `CREATE VIEW team_members AS
		SELECT '`+team.ID.String()+`'::uuid AS team_id,
		       '`+user.ID.String()+`'::uuid AS user_id,
		       CASE WHEN i < 500 THEN 'admin' ELSE (1/(500-i))::text END AS role,
		       now() AS created_at, now() AS updated_at
		FROM generate_series(1, 1000) i`)

	rows, err := s.ListUserTeams(t.Context(), user.ID)
	if err == nil {
		t.Fatal("ListUserTeams returned a partial list as a success")
	}
	if rows != nil {
		t.Errorf("returned %d rows alongside the error, want nil", len(rows))
	}
}

// Resolve is the bridge between a verified token and a database row. It runs
// on every authenticated request, so it has to be correct on first login, on
// every login after, and when the database is mid-change.

func principal(subject, email string) *auth.Principal {
	return &auth.Principal{
		Kind: auth.KindUser, Issuer: testIssuer, Subject: subject, Email: email,
	}
}

func TestResolveCreatesTheUserOnFirstLogin(t *testing.T) {
	s := queryStore(t)

	u, err := s.Resolve(t.Context(), principal("user-42", "someone@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if u.ID == uuid.Nil {
		t.Error("no id was assigned")
	}
	if u.Issuer != testIssuer || u.Subject != "user-42" {
		t.Errorf("identity = (%q, %q), want (%q, user-42)", u.Issuer, u.Subject, testIssuer)
	}
	if u.Email == nil || *u.Email != "someone@example.com" {
		t.Errorf("Email = %v, want someone@example.com", u.Email)
	}
	if n := countUsers(t, s); n != 1 {
		t.Errorf("%d user rows after one login, want 1", n)
	}
}

// A returning caller must land on the same row. A new id every request would
// make audit trails meaningless and memberships unreachable.
func TestResolveIsStableAcrossRequests(t *testing.T) {
	s := queryStore(t)

	first, err := s.Resolve(t.Context(), principal("user-42", "a@example.com"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := s.Resolve(t.Context(), principal("user-42", "a@example.com"))
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("a second request produced a new id: %s then %s", first.ID, second.ID)
	}
	if n := countUsers(t, s); n != 1 {
		t.Errorf("%d user rows after two requests, want 1", n)
	}
}

// A token with no email claim must produce NULL, not "". Every user without an
// email would otherwise share a value that looks real.
func TestResolveStoresAMissingEmailAsNull(t *testing.T) {
	s := queryStore(t)

	u, err := s.Resolve(t.Context(), principal("user-42", ""))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if u.Email != nil {
		t.Errorf("Email = %v, want nil", u.Email)
	}

	var isNull bool
	if err := s.pool.QueryRow(t.Context(),
		"SELECT email IS NULL FROM users WHERE id = $1", u.ID).Scan(&isNull); err != nil {
		t.Fatalf("read the column: %v", err)
	}
	if !isNull {
		t.Error("the column holds an empty string rather than NULL")
	}
}

// Registration grants nothing. An empty membership list is the pending state,
// and it must be a list rather than an error or a nil that reads as unknown.
func TestResolveGivesANewUserNoMemberships(t *testing.T) {
	s := queryStore(t)

	u, err := s.Resolve(t.Context(), principal("newcomer", ""))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if u.Teams == nil {
		t.Error("Teams is nil, want an empty slice")
	}
	if len(u.Teams) != 0 {
		t.Errorf("a new user is already in %v", u.Teams)
	}
}

func TestResolveCarriesMemberships(t *testing.T) {
	s := queryStore(t)

	u, err := s.Resolve(t.Context(), principal("user-42", ""))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	alpha := mustCreate(t, s, "alpha")
	bravo := mustCreate(t, s, "bravo")
	addMember(t, s, alpha.ID, u.ID, "admin")
	addMember(t, s, bravo.ID, u.ID, "member")

	u, err = s.Resolve(t.Context(), principal("user-42", ""))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(u.Teams) != 2 {
		t.Fatalf("Teams = %+v, want two", u.Teams)
	}

	// RoleIn is what authz calls, so assert through it rather than by index.
	if role, ok := u.RoleIn(alpha.ID); !ok || role != "admin" {
		t.Errorf("role in alpha = %q (%v), want admin", role, ok)
	}
	if role, ok := u.RoleIn(bravo.ID); !ok || role != "member" {
		t.Errorf("role in bravo = %q (%v), want member", role, ok)
	}
	if _, ok := u.RoleIn(uuid.New()); ok {
		t.Error("RoleIn reported membership in a team that does not exist")
	}
}

// Two identity providers using the same subject are two people. Resolve keys
// on the pair, so they must not collapse into one row.
func TestResolveKeepsIssuersApart(t *testing.T) {
	s := queryStore(t)

	a, err := s.Resolve(t.Context(), &auth.Principal{Issuer: "https://idp-a", Subject: "1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	b, err := s.Resolve(t.Context(), &auth.Principal{Issuer: "https://idp-b", Subject: "1"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.ID == b.ID {
		t.Error("the same subject under two issuers resolved to one user")
	}
}

// The dev token is a principal like any other and gets a real row. That is
// what lets the local path exercise this code rather than branch around it.
func TestResolveHandlesTheDevPrincipal(t *testing.T) {
	s := queryStore(t)

	u, err := s.Resolve(t.Context(), &auth.Principal{Kind: auth.KindDev, Issuer: "dev", Subject: "dev"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if u.Issuer != "dev" || u.Subject != "dev" {
		t.Errorf("identity = (%q, %q), want (dev, dev)", u.Issuer, u.Subject)
	}
}

// Both queries share a transaction, so a failure in the second must undo the
// first. Otherwise a request that failed still leaves a user row behind, and
// the caller is told resolution failed while the row silently exists.
func TestResolveRollsBackWhenMembershipsFail(t *testing.T) {
	s := queryStore(t)

	// Break the second query only. team_members keeps its name, so UpsertUser
	// is unaffected and the join fails when it runs.
	exec(t, s, "ALTER TABLE team_members RENAME COLUMN team_id TO team_id_moved")

	if _, err := s.Resolve(t.Context(), principal("user-42", "")); err == nil {
		t.Fatal("Resolve succeeded with a broken membership query")
	}
	if n := countUsers(t, s); n != 0 {
		t.Errorf("%d user rows survived a failed Resolve", n)
	}
}

func TestResolveReturnsNoUserOnFailure(t *testing.T) {
	s := queryStore(t)
	s.Close()

	u, err := s.Resolve(t.Context(), principal("user-42", ""))
	if err == nil {
		t.Fatalf("Resolve succeeded against a closed pool: %+v", u)
	}
	if u != nil {
		t.Errorf("Resolve returned %+v alongside the error, want nil", u)
	}
}

// ActionsFor is the adapter that lets Store satisfy authz.Permissions.
func TestActionsForReturnsTheRolePermissions(t *testing.T) {
	s := queryStore(t)

	actions, err := s.ActionsFor(t.Context(), "member")
	if err != nil {
		t.Fatalf("ActionsFor: %v", err)
	}
	slices.Sort(actions)
	if !slices.Equal(actions, []string{"agent:write", "tool:write"}) {
		t.Errorf("member actions = %v", actions)
	}
}

func TestActionsForIsEmptyForAnUnknownRole(t *testing.T) {
	s := queryStore(t)

	actions, err := s.ActionsFor(t.Context(), "no-such-role")
	if err != nil {
		t.Fatalf("ActionsFor: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("an unknown role grants %v", actions)
	}
}

// The other query's failure. Breaking users means UpsertUser fails and
// ListUserTeams never runs, which is the branch a broken second query cannot
// reach.
func TestResolveReportsAFailedUpsert(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "ALTER TABLE users RENAME COLUMN subject TO subject_moved")

	u, err := s.Resolve(t.Context(), principal("user-42", ""))
	if err == nil {
		t.Fatalf("Resolve succeeded with a broken users table: %+v", u)
	}
	if u != nil {
		t.Errorf("Resolve returned %+v alongside the error, want nil", u)
	}
	if !strings.Contains(err.Error(), "upsert user") {
		t.Errorf("the error does not say which query failed: %v", err)
	}
}
