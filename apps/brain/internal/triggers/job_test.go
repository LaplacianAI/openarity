package triggers

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/robfig/cron/v3"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The parser DBOS builds, seconds first.
func dbosParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

func TestTheCronsParseAsDBOSReadsThem(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 12, 1, 3, 7, 30, 0, time.UTC)
	for _, c := range []struct {
		spec       string
		gap        time.Duration
		hour, minu int
		sec        int
	}{
		{tickCron, time.Minute, -1, -1, 0},
		{hourlyCron, time.Hour, -1, 7, 0},
	} {
		s, err := dbosParser().Parse(c.spec)
		if err != nil {
			t.Fatalf("%q does not parse: %v", c.spec, err)
		}
		first := s.Next(at)
		if gap := s.Next(first).Sub(first); gap != c.gap || first.Second() != c.sec ||
			(c.minu >= 0 && first.Minute() != c.minu) || (c.hour >= 0 && first.Hour() != c.hour) {
			t.Errorf("%q ticks at %v, %v apart", c.spec, first, gap)
		}
	}
}

// The names are stored state: a schedule naming a workflow nobody registers
// never fires, and renaming one orphans what it recorded. None backfills: a
// missed tick has no work the next does not do.
func TestTheJobsSchedulesAreWhatTheyClaim(t *testing.T) {
	t.Parallel()
	j := NewJob(nil, nil, nil, quiet())
	if j.Name() != "triggers" {
		t.Errorf("Name = %q", j.Name())
	}
	want := map[string]string{
		tickSchedule: tickWorkflow, hourlySchedule: hourlyWorkflow,
	}
	specs := j.schedules()
	if len(specs) != len(want) {
		t.Fatalf("%d schedules, want %d", len(specs), len(want))
	}
	for _, s := range specs {
		if want[s.ScheduleName] != s.WorkflowName || s.AutomaticBackfill {
			t.Errorf("schedule %+v", s)
		}
		if !strings.HasPrefix(s.WorkflowName, "openarity.triggers.") {
			t.Errorf("%s is not named for its package", s.WorkflowName)
		}
	}
}

func TestAJobWithNoStoreOrNoWayToRunRefuses(t *testing.T) {
	t.Parallel()
	for _, j := range []*Job{NewJob(nil, func(dbos.Client, uuid.UUID) error { return nil }, http.DefaultClient, quiet())} {
		if _, err := j.Register(nil); err == nil || !strings.Contains(err.Error(), "every fire would fail") {
			t.Errorf("Register = %v", err)
		}
	}
}

func TestATickHandsOutABatchPerHundredUpToEight(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]int{0: 0, 1: 1, 100: 1, 101: 2, 799: 8, 5000: 8} {
		if got := batches(n); got != want {
			t.Errorf("batches(%d) = %d, want %d", n, got, want)
		}
	}
}

// --- in a running DBOS ---

func dbosSchema(t *testing.T) string {
	return fmt.Sprintf("dbos_test_%08x", crc32.ChecksumIEEE([]byte(t.Name())))
}

type launched struct {
	*world
	d        dbos.Context
	job      *Job
	enqueued *recorded
	schema   string
}

func launch(t *testing.T) *launched {
	t.Helper()
	w := newWorld(t)
	dsn := w.dsn
	schema := dbosSchema(t)
	d, err := dbos.NewContext(context.WithoutCancel(t.Context()), dbos.Config{
		AppName: "openarity-brain-test", DatabaseURL: dsn, DatabaseSchema: schema,
		ExecutorID: "w1", Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("dbos.NewContext: %v", err)
	}
	enqueued := &recorded{}
	j := NewJob(w.store, func(_ dbos.Client, id uuid.UUID) error { return enqueued.Enqueue(t.Context(), id) }, http.DefaultClient, quiet())
	j.now = func() time.Time { return w.clock }
	if _, err := j.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := d.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = dbos.Shutdown(d, 5*time.Second) }) })
	return &launched{world: w, d: d, job: j, enqueued: enqueued, schema: schema}
}

func TestATickFiresWhatIsDueThroughItsBatches(t *testing.T) {
	l := launch(t)
	v := l.make(t, hourly("summary"))
	l.clock = *v.NextFireAt

	h, err := dbos.RunWorkflow(l.d, l.job.tick, dbos.ScheduledWorkflowInput{ScheduledTime: l.clock})
	if err != nil {
		t.Fatalf("RunWorkflow(tick): %v", err)
	}
	minute, err := h.GetResult()
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	batch, err := dbos.RetrieveWorkflow[string](l.d, "fire:"+minute+":0")
	if err != nil {
		t.Fatalf("the tick handed out no batch: %v", err)
	}
	if out, err := batch.GetResult(); err != nil || !strings.HasSuffix(out, ": 1") {
		t.Fatalf("batch = %q, %v; want one fired", out, err)
	}
	if l.enqueued.count() != 1 || l.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID) != 1 {
		t.Errorf("enqueued %d, want the one run", l.enqueued.count())
	}

	// The same tick again hands out the same batch, which has run: nothing twice.
	h, err = dbos.RunWorkflow(l.d, l.job.tick, dbos.ScheduledWorkflowInput{ScheduledTime: l.clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.GetResult(); err != nil {
		t.Fatal(err)
	}
	if n := l.count(t, `SELECT count(*) FROM runs WHERE session_id = $1`, v.SessionID); n != 1 {
		t.Errorf("%d runs after the tick ran again, want 1", n)
	}
}

// The database's passing trouble is retried without counting against the
// schedule; an error of the schedule's own counts.
func TestOnlyTheDatabasesPassingTroubleGoesUncounted(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]bool{
		&pgconn.PgError{Code: "40001"}:                                            true, // serialization
		&pgconn.PgError{Code: "40P01"}:                                            true, // deadlock
		&pgconn.PgError{Code: "55P03"}:                                            true, // lock timeout
		&pgconn.PgError{Code: "57014"}:                                            true, // statement timeout
		&pgconn.PgError{Code: "08006"}:                                            true, // connection failure
		&pgconn.PgError{Code: "53300"}:                                            true, // too many connections
		fmt.Errorf("claim: %w", &pgconn.PgError{Code: "40001"}):                   true, // wrapped
		fmt.Errorf("x: %w", context.DeadlineExceeded):                             true,
		fmt.Errorf("x: %w", &net.OpError{Op: "dial", Err: errors.New("refused")}): true,
		&pgconn.PgError{Code: "23503"}:                                            false, // a key the schedule broke
		errors.New("read the schedule's conversation: no rows"):                   false,
	} {
		if got := passing(err); got != want {
			t.Errorf("passing(%v) = %v, want %v", err, got, want)
		}
	}
}
