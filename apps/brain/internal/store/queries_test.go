package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	teams, err := s.ListTeams(t.Context())
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

	teams, err := s.ListTeams(t.Context())
	if err != nil {
		t.Fatalf("ListTeams on an empty table: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("ListTeams returned %v, want nothing", teamNames(teams))
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

// There is no unique index on name. This test exists to make that a decision
// rather than an oversight — if a unique constraint is ever added, this test
// fails and forces the question of what the API should do about it.
func TestTeamNamesAreNotUnique(t *testing.T) {
	s := queryStore(t)

	first := mustCreate(t, s, "platform")
	second := mustCreate(t, s, "platform")

	if first.ID == second.ID {
		t.Fatal("two inserts produced one row")
	}
	if got := countTeams(t, s); got != 2 {
		t.Errorf("counted %d teams, want 2", got)
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
		inside, err := q.ListTeams(t.Context())
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

	for range maxConns * 2 {
		if err := s.InTx(ctx, func(q *db.Queries) error {
			_, err := q.CreateTeam(ctx, "committed")
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
