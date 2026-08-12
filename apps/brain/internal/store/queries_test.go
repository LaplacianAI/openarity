package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// queryStore is migrationStore with the schema actually applied, which is what
// every query test needs and no migration test wants.
func queryStore(t *testing.T) *Store {
	t.Helper()

	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// teamLister is satisfied by both *Store and the *db.Queries a transaction
// hands to its callback.
type teamLister interface {
	ListTeams(context.Context, db.ListTeamsParams) ([]db.Team, error)
}

// allTeams asks for every team in one page. ListTeams is paginated, but most
// of these tests are about ordering and error paths, not the cursor — the
// keyset behaviour has its own tests below.
func allTeams(ctx context.Context, q teamLister) ([]db.Team, error) {
	return q.ListTeams(ctx, db.ListTeamsParams{PageSize: 1000})
}

// teamNames reads the names out of a slice, so ordering assertions report
// something legible instead of four-field structs.
func teamNames(teams []db.Team) []string {
	names := make([]string, len(teams))
	for i, team := range teams {
		names[i] = team.Name
	}
	return names
}

func mustCreate(t *testing.T, s *Store, name string) db.Team {
	t.Helper()

	team, err := s.CreateTeam(t.Context(), name)
	if err != nil {
		t.Fatalf("CreateTeam(%q): %v", name, err)
	}
	return team
}

// backdate moves a row's created_at so ordering tests do not depend on how
// fast the machine inserts. Three inserts in the same millisecond is not a
// hypothetical.
func backdate(t *testing.T, s *Store, id uuid.UUID, at time.Time) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(), "UPDATE teams SET created_at = $1 WHERE id = $2", at, id); err != nil {
		t.Fatalf("backdate %s: %v", id, err)
	}
}

// exec runs DDL against this test's own schema. Only the tests that
// deliberately break the schema need it.
func exec(t *testing.T, s *Store, sql string) {
	t.Helper()

	if _, err := s.pool.Exec(t.Context(), sql); err != nil {
		t.Fatalf("exec %.40s…: %v", sql, err)
	}
}

// countTeams goes through the pool rather than the generated query, so it sees
// only committed rows. That is the whole point of it in the transaction tests.
func countTeams(t *testing.T, s *Store) int {
	t.Helper()

	var n int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM teams").Scan(&n); err != nil {
		t.Fatalf("count teams: %v", err)
	}
	return n
}

// The base case. Everything else in this file asserts an edge, so without one
// plain round-trip a CreateTeam that never persisted would pass the suite.
func TestCreateTeamRoundTrips(t *testing.T) {
	s := queryStore(t)

	created := mustCreate(t, s, "platform")

	got, err := s.GetTeam(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetTeam returned id %s, want %s", got.ID, created.ID)
	}
	if got.Name != "platform" {
		t.Errorf("GetTeam returned name %q, want %q", got.Name, "platform")
	}
}

// RETURNING * is the reason CreateTeam returns a row rather than an id. If it
// ever becomes RETURNING id, the defaulted columns come back as zero values and
// every caller silently starts seeing a zero timestamp.
func TestCreateTeamReturnsTheDefaultedColumns(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")

	if team.ID == uuid.Nil {
		t.Error("CreateTeam returned the zero uuid — gen_random_uuid did not run, or RETURNING dropped the column")
	}
	if team.CreatedAt.IsZero() {
		t.Error("CreateTeam returned a zero created_at")
	}
	if team.UpdatedAt.IsZero() {
		t.Error("CreateTeam returned a zero updated_at")
	}
}

// Two teams created in a row must not share an id. A default that failed to
// generate would hand back the same value twice, and the primary key would only
// complain on the second insert — after the first had already been trusted.
func TestCreateTeamGeneratesADistinctID(t *testing.T) {
	s := queryStore(t)

	first := mustCreate(t, s, "first")
	second := mustCreate(t, s, "second")

	if first.ID == second.ID {
		t.Errorf("both teams got id %s", first.ID)
	}
}

// The name is what an operator recognises a team by, and the API publishes it.
// Two teams sharing one are indistinguishable in every listing, so the schema
// refuses rather than the handler.
func TestCreateTeamRefusesADuplicateName(t *testing.T) {
	s := queryStore(t)

	mustCreate(t, s, "platform")

	_, err := s.CreateTeam(t.Context(), "platform")
	if err == nil {
		t.Fatal("a second team took the same name")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("CreateTeam returned %v, want a Postgres error", err)
	}
	if pgErr.Code != "23505" {
		t.Errorf("SQLSTATE %s, want 23505 — the handler maps that code to 409", pgErr.Code)
	}
}

// The constraint is on the exact name, so names differing only by case or
// surrounding space are distinct rows. Handlers trim before inserting; this
// records that the database does not fold case, which would surprise anyone
// who assumed it did.
func TestTeamNamesAreUniqueExactly(t *testing.T) {
	s := queryStore(t)

	mustCreate(t, s, "platform")

	if _, err := s.CreateTeam(t.Context(), "Platform"); err != nil {
		t.Errorf("CreateTeam(%q) after %q: %v", "Platform", "platform", err)
	}
}

// A missing row is pgx.ErrNoRows, not a zero Team and a nil error. Callers
// branch on this, so it is part of the contract rather than an implementation
// detail of :one.
func TestGetTeamReportsAMissingRow(t *testing.T) {
	s := queryStore(t)

	_, err := s.GetTeam(t.Context(), uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetTeam for an unknown id returned %v, want pgx.ErrNoRows", err)
	}
}

// ORDER BY created_at DESC, asserted against rows whose timestamps are far
// enough apart that insertion order cannot be doing the work. Inserting three
// rows and checking they come back in insertion order proves nothing — that is
// what an unordered scan returns anyway.
func TestListTeamsReturnsNewestFirst(t *testing.T) {
	s := queryStore(t)

	now := time.Now()
	oldest := mustCreate(t, s, "oldest")
	middle := mustCreate(t, s, "middle")
	newest := mustCreate(t, s, "newest")

	// Deliberately the reverse of insertion order, so a query with no ORDER BY
	// clause has to get it wrong.
	backdate(t, s, oldest.ID, now.Add(-3*time.Hour))
	backdate(t, s, middle.ID, now.Add(-2*time.Hour))
	backdate(t, s, newest.ID, now.Add(-1*time.Hour))

	teams, err := allTeams(t.Context(), s)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}

	want := []string{"newest", "middle", "oldest"}
	got := teamNames(teams)
	if len(got) != len(want) {
		t.Fatalf("ListTeams returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListTeams returned %v, want %v", got, want)
		}
	}
}

// An empty table is an empty slice and a nil error, never an error. Handlers
// paginate over this, and a query that errored on empty would only fail on a
// fresh install.
func TestListTeamsOnAnEmptyTable(t *testing.T) {
	s := queryStore(t)

	teams, err := allTeams(t.Context(), s)
	if err != nil {
		t.Fatalf("ListTeams on an empty table: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("ListTeams returned %v, want nothing", teamNames(teams))
	}
}

// The three error paths in a generated :many are the ones a caller depends on
// most and sees least: each returns `nil, err`, so a query that half-failed
// must never come back as a short list and a nil error. They are only
// reachable by breaking the database out from under the query, which is what
// the next three tests do — each one lands on a different branch.
//
// The schema is this test's own and is dropped afterwards, so mangling it is
// free.

// Branch one: the query never runs.
func TestListTeamsReportsAQueryFailure(t *testing.T) {
	s := queryStore(t)
	s.Close()

	teams, err := allTeams(t.Context(), s)
	if err == nil {
		t.Fatal("ListTeams succeeded against a closed pool")
	}
	if teams != nil {
		t.Errorf("ListTeams returned %v alongside an error, want nil", teamNames(teams))
	}
}

// Branch two: the query runs and a row will not scan. Widening id to text and
// putting a non-uuid in it makes the generated Scan fail on the first row.
func TestListTeamsReportsAScanFailure(t *testing.T) {
	s := queryStore(t)

	// team_members has a foreign key to teams(id); a uuid column cannot be
	// widened to text while something references it.
	exec(t, s, "DROP TABLE team_members")
	exec(t, s, "ALTER TABLE teams ALTER COLUMN id TYPE text USING id::text")
	exec(t, s, "INSERT INTO teams (id, name) VALUES ('not-a-uuid', 'broken')")

	teams, err := allTeams(t.Context(), s)
	if err == nil {
		t.Fatal("ListTeams accepted a row it cannot scan")
	}
	if teams != nil {
		t.Errorf("ListTeams returned %v alongside an error, want nil", teamNames(teams))
	}
}

// Branch three: rows start arriving and then the server raises. Postgres
// streams a view, so the division by zero at row 500 lands after 499 good rows
// have already been scanned — the error surfaces from rows.Err(), not from
// Query. This is the branch that turns a partial result into an error, and the
// only one where a caller could otherwise be handed half a list.
func TestListTeamsReportsAFailureMidStream(t *testing.T) {
	s := queryStore(t)

	// CASCADE because team_members references teams. Without it the drop
	// fails and the view below is never created, so the test asserts nothing.
	exec(t, s, "DROP TABLE teams CASCADE")
	exec(t, s, `CREATE VIEW teams AS
		SELECT gen_random_uuid() AS id,
		       CASE WHEN i < 500 THEN 'ok' ELSE (1/(500-i))::text END AS name,
		       now() AS created_at, now() AS updated_at
		FROM generate_series(1, 1000) i`)

	teams, err := allTeams(t.Context(), s)
	if err == nil {
		t.Fatal("ListTeams returned a partial result as success")
	}
	if teams != nil {
		t.Errorf("ListTeams returned %d rows alongside an error, want nil", len(teams))
	}
}

// The cursor predicate is the part of ListTeams that nothing else exercises:
// the handler tests drive a fake, and a fake can be wrong in exactly the way
// the SQL is. These run against Postgres.

// Without a cursor the WHERE clause must let every row through, or the first
// page of every listing is empty.
func TestListTeamsIgnoresTheCursorWhenUnused(t *testing.T) {
	s := queryStore(t)

	mustCreate(t, s, "first")
	mustCreate(t, s, "second")

	// AfterCreatedAt and AfterID are the zero values a caller sends when there
	// is no cursor. With UseCursor false they must not filter anything.
	teams, err := s.ListTeams(t.Context(), db.ListTeamsParams{PageSize: 10})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Errorf("got %v, want both teams", teamNames(teams))
	}
}

func TestListTeamsRespectsThePageSize(t *testing.T) {
	s := queryStore(t)

	for _, name := range []string{"a", "b", "c"} {
		mustCreate(t, s, name)
	}

	teams, err := s.ListTeams(t.Context(), db.ListTeamsParams{PageSize: 2})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Errorf("got %d rows for PageSize 2, want 2", len(teams))
	}
}

// Walking the pages must yield every team exactly once. An off-by-one in the
// row comparison shows up here as a repeat or a gap, and nowhere else.
func TestListTeamsPagesThroughEveryRow(t *testing.T) {
	s := queryStore(t)

	now := time.Now()
	want := map[string]bool{}
	for i := range 5 {
		team := mustCreate(t, s, string(rune('a'+i)))
		backdate(t, s, team.ID, now.Add(-time.Duration(i)*time.Hour))
		want[team.Name] = true
	}

	seen := map[string]int{}
	params := db.ListTeamsParams{PageSize: 2}

	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging did not terminate")
		}

		rows, err := s.ListTeams(t.Context(), params)
		if err != nil {
			t.Fatalf("ListTeams: %v", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			seen[row.Name]++
		}

		last := rows[len(rows)-1]
		params.UseCursor = true
		params.AfterCreatedAt = last.CreatedAt
		params.AfterID = last.ID
	}

	if len(seen) != len(want) {
		t.Errorf("saw %d distinct teams, want %d", len(seen), len(want))
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("team %q appeared %d times", name, n)
		}
	}
}

// Rows sharing created_at are why the comparison is a row constructor rather
// than two ANDed inequalities. With `created_at < x AND id < y` the tied rows
// are dropped; with a row constructor they are ordered by id and paged
// through.
func TestListTeamsPagesThroughRowsSharingATimestamp(t *testing.T) {
	s := queryStore(t)

	at := time.Now().Add(-time.Hour)
	for _, name := range []string{"a", "b", "c"} {
		team := mustCreate(t, s, name)
		backdate(t, s, team.ID, at)
	}

	seen := map[string]int{}
	params := db.ListTeamsParams{PageSize: 1}

	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging did not terminate on tied timestamps")
		}

		rows, err := s.ListTeams(t.Context(), params)
		if err != nil {
			t.Fatalf("ListTeams: %v", err)
		}
		if len(rows) == 0 {
			break
		}
		seen[rows[0].Name]++

		params.UseCursor = true
		params.AfterCreatedAt = rows[0].CreatedAt
		params.AfterID = rows[0].ID
	}

	if len(seen) != 3 {
		t.Errorf("saw %d of 3 teams sharing a timestamp: %v", len(seen), seen)
	}
}

// A cursor must exclude the row it points at, or every page repeats its own
// last row forever.
func TestListTeamsExcludesTheCursorRow(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "only")

	rows, err := s.ListTeams(t.Context(), db.ListTeamsParams{
		PageSize:       10,
		UseCursor:      true,
		AfterCreatedAt: team.CreatedAt,
		AfterID:        team.ID,
	})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("the cursor row came back again: %v", teamNames(rows))
	}
}

// Members are ordered by subject, and two users from different issuers can
// share one — users is unique on (issuer, subject), not on subject. The id
// tiebreak is what keeps that pair pageable.
func TestListTeamMembersPagesThroughASharedSubject(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")
	for _, issuer := range []string{"okta", "github", "google"} {
		user, err := s.UpsertUser(t.Context(), db.UpsertUserParams{Issuer: issuer, Subject: "bob"})
		if err != nil {
			t.Fatalf("UpsertUser(%s): %v", issuer, err)
		}
		if _, err := s.AddTeamMember(t.Context(), db.AddTeamMemberParams{
			TeamID: team.ID, UserID: user.ID, Role: "developer",
		}); err != nil {
			t.Fatalf("AddTeamMember: %v", err)
		}
	}

	seen := map[uuid.UUID]int{}
	params := db.ListTeamMembersParams{TeamID: team.ID, PageSize: 1}

	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("paging did not terminate on a shared subject")
		}

		rows, err := s.ListTeamMembers(t.Context(), params)
		if err != nil {
			t.Fatalf("ListTeamMembers: %v", err)
		}
		if len(rows) == 0 {
			break
		}
		seen[rows[0].ID]++

		params.UseCursor = true
		params.AfterSubject = rows[0].Subject
		params.AfterID = rows[0].ID
	}

	if len(seen) != 3 {
		t.Errorf("saw %d of 3 users sharing the subject %q", len(seen), "bob")
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("user %s appeared %d times", id, n)
		}
	}
}

// The generated :many returns `nil, err` on every failure, so a half-read
// listing must never come back as a short list and a nil error. ListTeams has
// the same three branches covered above; these are ListTeamMembers'.

// Branch one: the query never runs.
func TestListTeamMembersReportsAQueryFailure(t *testing.T) {
	s := queryStore(t)
	s.Close()

	rows, err := s.ListTeamMembers(t.Context(), db.ListTeamMembersParams{
		TeamID: uuid.New(), PageSize: 10,
	})
	if err == nil {
		t.Fatal("ListTeamMembers succeeded against a closed pool")
	}
	if rows != nil {
		t.Errorf("returned %d rows alongside an error, want nil", len(rows))
	}
}

// Branch two: the query runs and a row will not scan. Widening users.id to
// text and putting a non-uuid in it makes the generated Scan fail.
func TestListTeamMembersReportsAScanFailure(t *testing.T) {
	s := queryStore(t)

	team := mustCreate(t, s, "platform")

	// team_members references users(id); the column cannot be widened while
	// that foreign key stands.
	// Both sides of the join have to be widened: the predicate is
	// u.id = tm.user_id, and a text-to-uuid comparison fails at planning time
	// rather than reaching the scan this test is about.
	exec(t, s, "ALTER TABLE team_members DROP CONSTRAINT team_members_user_id_fkey")
	exec(t, s, "ALTER TABLE users ALTER COLUMN id TYPE text USING id::text")
	exec(t, s, "ALTER TABLE team_members ALTER COLUMN user_id TYPE text USING user_id::text")
	exec(t, s, "INSERT INTO users (id, issuer, subject) VALUES ('not-a-uuid', 'okta', 'broken')")
	exec(t, s, "INSERT INTO team_members (team_id, user_id, role) "+
		"VALUES ('"+team.ID.String()+"', 'not-a-uuid', 'admin')")

	rows, err := s.ListTeamMembers(t.Context(), db.ListTeamMembersParams{
		TeamID: team.ID, PageSize: 10,
	})
	if err == nil {
		t.Fatal("ListTeamMembers accepted a row it cannot scan")
	}
	if rows != nil {
		t.Errorf("returned %d rows alongside an error, want nil", len(rows))
	}
}

// Branch three: rows start arriving and then the server raises. Postgres
// streams the view, so the division by zero at row 500 lands after 499 good
// rows have already been scanned — the error surfaces from rows.Err(). This is
// the only branch where a caller could otherwise be handed half a listing.
func TestListTeamMembersReportsAFailureMidStream(t *testing.T) {
	s := queryStore(t)

	exec(t, s, "DROP TABLE team_members")
	exec(t, s, `CREATE VIEW team_members AS
		SELECT gen_random_uuid() AS team_id, u.id AS user_id,
		       CASE WHEN i < 500 THEN 'admin' ELSE (1/(500-i))::text END AS role,
		       now() AS created_at, now() AS updated_at
		FROM generate_series(1, 1000) i, users u`)

	// One user, so the view's cross join yields exactly the 1000 rows above.
	if _, err := s.UpsertUser(t.Context(), db.UpsertUserParams{Issuer: "okta", Subject: "a"}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	rows, err := s.ListTeamMembers(t.Context(), db.ListTeamMembersParams{
		TeamID: uuid.New(), PageSize: 10000,
	})
	if err == nil {
		t.Fatal("ListTeamMembers returned a partial result as success")
	}
	if rows != nil {
		t.Errorf("returned %d rows alongside an error, want nil", len(rows))
	}
}

// The query is scoped to one team. A missing WHERE would return the other
// team's members, which is a cross-team leak rather than a paging bug.
func TestListTeamMembersReturnsOnlyThatTeam(t *testing.T) {
	s := queryStore(t)

	mine := mustCreate(t, s, "mine")
	theirs := mustCreate(t, s, "theirs")

	for i, team := range []db.Team{mine, theirs} {
		user, err := s.UpsertUser(t.Context(), db.UpsertUserParams{
			Issuer: "okta", Subject: string(rune('a' + i)),
		})
		if err != nil {
			t.Fatalf("UpsertUser: %v", err)
		}
		if _, err := s.AddTeamMember(t.Context(), db.AddTeamMemberParams{
			TeamID: team.ID, UserID: user.ID, Role: "admin",
		}); err != nil {
			t.Fatalf("AddTeamMember: %v", err)
		}
	}

	rows, err := s.ListTeamMembers(t.Context(), db.ListTeamMembersParams{
		TeamID: mine.ID, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListTeamMembers: %v", err)
	}
	if len(rows) != 1 || rows[0].Subject != "a" {
		t.Errorf("got %+v, want only this team's member", rows)
	}
}

func TestDeleteTeamRemovesOnlyItsOwnRow(t *testing.T) {
	s := queryStore(t)

	doomed := mustCreate(t, s, "doomed")
	survivor := mustCreate(t, s, "survivor")

	if err := s.DeleteTeam(t.Context(), doomed.ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}

	if _, err := s.GetTeam(t.Context(), doomed.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("deleted team is still readable (err=%v)", err)
	}
	if _, err := s.GetTeam(t.Context(), survivor.ID); err != nil {
		t.Errorf("DeleteTeam removed the wrong row: %v", err)
	}
}

// :exec reports no error when it matches nothing, because DELETE of zero rows
// is not a failure in SQL. Worth pinning: a caller that wants "did it exist"
// has to check first, and this is where that surprise is documented.
func TestDeleteTeamIsSilentOnAMissingRow(t *testing.T) {
	s := queryStore(t)

	if err := s.DeleteTeam(t.Context(), uuid.New()); err != nil {
		t.Fatalf("DeleteTeam on an unknown id: %v", err)
	}
}

func TestInTxCommitsWhenTheCallbackSucceeds(t *testing.T) {
	s := queryStore(t)

	err := s.InTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.CreateTeam(t.Context(), "first"); err != nil {
			return err
		}
		_, err := q.CreateTeam(t.Context(), "second")
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	if got := countTeams(t, s); got != 2 {
		t.Errorf("counted %d teams after a committed transaction, want 2", got)
	}
}

// The reason InTx exists. A write that succeeded before the failure must not
// survive it — otherwise callers get half a change and no way to know.
func TestInTxRollsBackEverythingWhenTheCallbackFails(t *testing.T) {
	s := queryStore(t)

	sentinel := errors.New("callback failed")

	err := s.InTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.CreateTeam(t.Context(), "written before the failure"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the callback's error", err)
	}

	if got := countTeams(t, s); got != 0 {
		t.Errorf("counted %d teams after a failed transaction, want 0", got)
	}
}

// The deferred Rollback has to survive a panic, not just an error return.
// Without it the connection goes back to the pool with an open transaction and
// poisons whoever picks it up next.
func TestInTxRollsBackWhenTheCallbackPanics(t *testing.T) {
	s := queryStore(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate out of InTx")
			}
		}()

		_ = s.InTx(t.Context(), func(q *db.Queries) error {
			if _, err := q.CreateTeam(t.Context(), "written before the panic"); err != nil {
				t.Errorf("CreateTeam inside the transaction: %v", err)
			}
			panic("boom")
		})
	}()

	if got := countTeams(t, s); got != 0 {
		t.Errorf("counted %d teams after a panic, want 0", got)
	}
}

// Proves the callback's *db.Queries is bound to the transaction and not to the
// pool. If InTx ever handed over s.Queries by mistake, every test above would
// still pass — the writes would land, just not transactionally.
func TestInTxWritesAreInvisibleUntilCommit(t *testing.T) {
	s := queryStore(t)

	err := s.InTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.CreateTeam(t.Context(), "uncommitted"); err != nil {
			return err
		}

		// Same database, different connection: it must not see the row yet.
		if got := countTeams(t, s); got != 0 {
			t.Errorf("an outside reader saw %d rows mid-transaction, want 0 — the callback is writing through the pool", got)
		}

		// The transaction must see its own write.
		inside, err := allTeams(t.Context(), q)
		if err != nil {
			return err
		}
		if len(inside) != 1 {
			t.Errorf("the transaction sees %d of its own rows, want 1", len(inside))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	if got := countTeams(t, s); got != 1 {
		t.Errorf("counted %d teams after commit, want 1", got)
	}
}

// If the transaction cannot even start, the callback must not run. Running it
// against the pool instead would be the worst possible fallback: the writes
// would land, unprotected, and the caller would be told the transaction
// failed.
func TestInTxDoesNotRunTheCallbackWhenBeginFails(t *testing.T) {
	s := queryStore(t)
	s.Close()

	called := false
	err := s.InTx(t.Context(), func(_ *db.Queries) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("InTx succeeded against a closed pool")
	}
	if called {
		t.Error("InTx ran the callback after Begin failed")
	}
}

// Every path through InTx must hand its connection back. A leak only shows up
// under load, as the pool slowly starves, which is a miserable thing to debug
// from production.
//
// The deadline is the point: a leak does not make Begin fail, it makes Begin
// block once the pool is empty. Without a bounded context this test hangs
// until Go's ten-minute timeout instead of failing in seconds.
func TestInTxReleasesItsConnection(t *testing.T) {
	s := queryStore(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	failure := errors.New("no")

	for i := range maxConns * 2 {
		// A distinct name per round: teams.name is unique, so reusing one
		// would fail the second insert for a reason that has nothing to do
		// with connections.
		name := fmt.Sprintf("committed-%d", i)
		if err := s.InTx(ctx, func(q *db.Queries) error {
			_, err := q.CreateTeam(ctx, name)
			return err
		}); err != nil {
			t.Fatalf("committing InTx: %v — a leaked connection starves the pool", err)
		}

		if err := s.InTx(ctx, func(_ *db.Queries) error {
			return failure
		}); !errors.Is(err, failure) {
			t.Fatalf("rolling back InTx returned %v", err)
		}
	}

	if held := s.pool.Stat().AcquiredConns(); held != 0 {
		t.Errorf("%d connections still checked out after %d transactions", held, maxConns*4)
	}
}
