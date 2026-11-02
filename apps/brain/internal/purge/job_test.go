package purge

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// dbosParser is the parser DBOS builds, seconds first.
func dbosParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour |
		cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

func TestThePurgeCronParsesAsDBOSWillReadIt(t *testing.T) {
	t.Parallel()

	schedule, err := dbosParser().Parse(purgeCron)
	if err != nil {
		t.Fatalf("purgeCron = %q does not parse: %v", purgeCron, err)
	}
	first := schedule.Next(time.Date(2026, 11, 2, 3, 7, 0, 0, time.UTC))
	if got := schedule.Next(first).Sub(first); got != time.Hour || first.Minute() != 0 || first.Second() != 0 {
		t.Errorf("ticks at %v, %v apart; want on the hour, an hour apart", first, got)
	}
}

// The names are stored state: a schedule naming a workflow nobody registers
// never fires, and renaming either orphans the rows already written.
func TestTheJobRegistersOneHourlySchedule(t *testing.T) {
	t.Parallel()

	j := Job(nil, time.Hour, discardLogger())
	if j.Name() != "teams.purge" {
		t.Errorf("Name = %q", j.Name())
	}
	specs := j.schedules()
	if len(specs) != 1 {
		t.Fatalf("schedules() = %d, want 1", len(specs))
	}
	s := specs[0]
	if s.ScheduleName != "team-purge" || s.WorkflowName != "openarity.teams.purge" || s.Schedule != "0 0 * * * *" {
		t.Errorf("schedule = %+v", s)
	}
	if s.AutomaticBackfill {
		t.Error("the purge backfills, but a missed tick has no work the next one will not do")
	}
}

// --- against Postgres ---

func liveDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

type world struct {
	store *store.Store
	raw   *pgx.Conn
}

// newWorld is a migrated schema of the test's own, dropped afterwards.
func newWorld(t *testing.T) *world {
	t.Helper()

	base := liveDSN(t)
	schema := "brain_purge_" + strings.ToLower(t.Name())
	drop := "DROP SCHEMA IF EXISTS " + schema + " CASCADE"

	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, sql := range []string{drop, "CREATE SCHEMA " + schema} {
		if _, err := admin.Exec(t.Context(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	_ = admin.Close(t.Context())
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, drop); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := store.New(t.Context(), u.String())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close(context.WithoutCancel(t.Context())) })
	return &world{store: s, raw: raw}
}

// deletedTeam is a team marked deleted ago before the database's own now, so
// no comparison here is between Go's clock and Postgres's.
func (w *world) deletedTeam(t *testing.T, name string, ago time.Duration) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	if err := w.raw.QueryRow(t.Context(), `
		INSERT INTO teams (name, deleted_at) VALUES ($1, now() - make_interval(secs => $2))
		RETURNING id`, name, ago.Seconds()).Scan(&id); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	return id
}

func (w *world) now(t *testing.T) time.Time {
	t.Helper()

	var now time.Time
	if err := w.raw.QueryRow(t.Context(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("now: %v", err)
	}
	return now
}

func (w *world) exists(t *testing.T, id uuid.UUID) bool {
	t.Helper()

	var n int
	if err := w.raw.QueryRow(t.Context(), `SELECT count(*) FROM teams WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n == 1
}

// counting records the size of every batch it was asked to purge.
type counting struct {
	Store
	batches []int
}

func (c *counting) PurgeTeams(ctx context.Context, arg db.PurgeTeamsParams) ([]uuid.UUID, error) {
	ids, err := c.Store.PurgeTeams(ctx, arg)
	c.batches = append(c.batches, len(ids))
	return ids, err
}

func TestPurgeTeamsLoopsUntilNothingIsLeft(t *testing.T) {
	w := newWorld(t)
	for i := range 25 {
		w.deletedTeam(t, "gone-"+string(rune('a'+i)), 2*time.Hour)
	}
	live := w.deletedTeam(t, "recent", time.Minute)

	c := &counting{Store: w.store}
	n, err := Teams(t.Context(), c, w.now(t), time.Hour, discardLogger())
	if err != nil {
		t.Fatalf("Teams: %v", err)
	}
	if n != 25 {
		t.Errorf("purged %d, want 25", n)
	}
	if len(c.batches) != 3 || c.batches[0] != batchSize || c.batches[1] != batchSize || c.batches[2] != 5 {
		t.Errorf("batches = %v, want [10 10 5]", c.batches)
	}
	if !w.exists(t, live) {
		t.Error("a team still inside its window was purged")
	}
}

// Review focus 4: the window is the caller's, read on every tick. The same
// team survives a week's window and goes under an hour's.
func TestPurgeUsesTheWindowItIsGiven(t *testing.T) {
	w := newWorld(t)
	id := w.deletedTeam(t, "old", 2*time.Hour)

	if n, err := Teams(t.Context(), w.store, w.now(t), 168*time.Hour, discardLogger()); err != nil || n != 0 {
		t.Fatalf("Teams(168h) = %d, %v; want nothing purged", n, err)
	}
	if !w.exists(t, id) {
		t.Fatal("purged under a week's window, two hours after the delete")
	}

	if n, err := Teams(t.Context(), w.store, w.now(t), time.Hour, discardLogger()); err != nil || n != 1 {
		t.Fatalf("Teams(1h) = %d, %v; want the team purged", n, err)
	}
	if w.exists(t, id) {
		t.Error("still there under an hour's window")
	}
}

// Review focus 5: one team's cascade failing costs only that team. The rest
// of the tick purges, the error names the team, and the next tick tries it
// again.
func TestOneFailingTeamDoesNotHoldBackTheOthers(t *testing.T) {
	w := newWorld(t)
	stuck := w.deletedTeam(t, "stuck", 3*time.Hour) // oldest, so in the first batch
	var others []uuid.UUID
	for i := range 11 {
		others = append(others, w.deletedTeam(t, "other-"+string(rune('a'+i)), 2*time.Hour))
	}

	if _, err := w.raw.Exec(t.Context(), `
		CREATE FUNCTION refuse_purge() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = '`+stuck.String()+`' THEN RAISE EXCEPTION 'cascade failed'; END IF;
			RETURN OLD;
		END $$;
		CREATE TRIGGER refuse_purge BEFORE DELETE ON teams FOR EACH ROW EXECUTE FUNCTION refuse_purge();`); err != nil {
		t.Fatalf("install the failing trigger: %v", err)
	}

	var logs bytes.Buffer
	n, err := Teams(t.Context(), w.store, w.now(t), time.Hour, slog.New(slog.NewTextHandler(&logs, nil)))

	if err == nil || !strings.Contains(err.Error(), stuck.String()) {
		t.Errorf("err = %v, want it to name %s", err, stuck)
	}
	if !strings.Contains(logs.String(), "team_id="+stuck.String()) {
		t.Errorf("the failure was not logged with the team's id:\n%s", logs.String())
	}
	if n != len(others) {
		t.Errorf("purged %d, want the %d that could go", n, len(others))
	}
	for _, id := range others {
		if w.exists(t, id) {
			t.Errorf("team %s was held back by another team's failure", id)
		}
	}
	if !w.exists(t, stuck) {
		t.Fatal("the failing team is gone")
	}

	if _, err := w.raw.Exec(t.Context(), `DROP TRIGGER refuse_purge ON teams`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if n, err := Teams(t.Context(), w.store, w.now(t), time.Hour, discardLogger()); err != nil || n != 1 {
		t.Errorf("next tick = %d, %v; want the team that failed purged", n, err)
	}
}
