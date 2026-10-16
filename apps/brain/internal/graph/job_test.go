package graph

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// dbosParser is the parser DBOS builds, seconds first, so the constant is
// read here by the thing that will read it in the worker.
func dbosParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour |
		cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

func drainTicks(t *testing.T) (first, second time.Time) {
	t.Helper()

	schedule, err := dbosParser().Parse(drainCron)
	if err != nil {
		t.Fatalf("drainCron = %q does not parse: %v", drainCron, err)
	}
	first = schedule.Next(time.Date(2026, 9, 1, 3, 7, 12, 0, time.UTC))
	return first, schedule.Next(first)
}

// --- the schedule ---

// Once a minute, on the minute. Every tick is a row DBOS keeps and nothing
// deletes: measured, a schedule leaves one workflow row and one step row a
// tick. At ten seconds that is 8,640 a day for a job whose state is already
// the outbox; at a minute it is 1,440.
func TestTheDrainCronFiresOnceAMinute(t *testing.T) {
	t.Parallel()

	first, second := drainTicks(t)
	if got := second.Sub(first); got != time.Minute {
		t.Errorf("consecutive ticks are %v apart, want 1m: %q", got, drainCron)
	}
	if first.Second() != 0 {
		t.Errorf("the first tick is at %v, want the top of a minute", first)
	}
}

func TestTheFiveFieldDrainCronWouldBeRefused(t *testing.T) {
	t.Parallel()

	if _, err := dbosParser().Parse("* * * * *"); err == nil {
		t.Fatal("the five-field form parsed, so the six-field constant is not load-bearing")
	}
}

// A row that failed is claimable again once retryAfter has passed. If that
// were a whole tick, whether the next tick found it due would depend on which
// of two timestamps a few milliseconds apart came first, and a retry would
// take one minute or two at random.
func TestAFailedRowIsDueAgainByTheNextTick(t *testing.T) {
	t.Parallel()

	first, second := drainTicks(t)
	if gap := second.Sub(first); retryAfter <= 0 || retryAfter > gap/2 {
		t.Errorf("retryAfter is %v and ticks are %v apart: a failed row may miss the next tick", retryAfter, gap)
	}
}

// The alarm is raised on the ticks that land on alarmEvery. If no tick ever
// did, there would be no alarm.
func TestSomeTickLandsOnTheAlarm(t *testing.T) {
	t.Parallel()

	schedule, err := dbosParser().Parse(drainCron)
	if err != nil {
		t.Fatal(err)
	}
	var landed int
	tick := time.Date(2026, 9, 1, 3, 7, 12, 0, time.UTC)
	for range 240 {
		tick = schedule.Next(tick)
		if tick.Truncate(alarmEvery).Equal(tick) {
			landed++
		}
	}
	if want := int(4 * time.Hour / alarmEvery); landed != want {
		t.Errorf("%d of four hours of ticks land on the alarm, want %d", landed, want)
	}
}

func TestTheDrainJobRegistersOneScheduleThatDoesNotBackfill(t *testing.T) {
	t.Parallel()

	specs := DrainJob(&Projector{}, slog.New(slog.DiscardHandler)).schedules()
	if len(specs) != 1 {
		t.Fatalf("schedules() returned %d, want 1", len(specs))
	}

	spec := specs[0]
	if spec.ScheduleName != drainSchedule || spec.Schedule != drainCron || spec.WorkflowName != drainWorkflow {
		t.Errorf("spec = %+v", spec)
	}
	// A drain takes whatever is in the outbox, so a tick that was missed has
	// nothing of its own to do. Replaying an hour of them after a restart
	// would be sixty workflows, fifty-nine of which find the outbox empty.
	if spec.AutomaticBackfill {
		t.Error("the drain backfills missed ticks, and a missed tick carries no work")
	}
}

// Stored state: DBOS keeps the schedule and the workflow by these names, and
// a renamed one leaves a schedule pointing at a workflow nobody registered.
func TestTheDrainNamesAreTheOnesInTheDatabase(t *testing.T) {
	t.Parallel()

	if drainWorkflow != "openarity.graph.drain" || drainSchedule != "graph-drain" {
		t.Errorf("workflow %q, schedule %q", drainWorkflow, drainSchedule)
	}
	if got := DrainJob(nil, nil).Name(); got != "graph.drain" {
		t.Errorf("Name = %q", got)
	}
}

func TestADrainJobWithNoProjectorRefusesToRegister(t *testing.T) {
	t.Parallel()

	specs, err := DrainJob(nil, slog.New(slog.DiscardHandler)).Register(nil)
	if err == nil || specs != nil {
		t.Fatalf("Register accepted a job with no projector: %v, %v", specs, err)
	}
}

// --- a tick ---

var (
	onTheQuarter  = time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC)
	offTheQuarter = time.Date(2026, 9, 1, 10, 16, 0, 0, time.UTC)
)

func (w *world) job() drainJob {
	return DrainJob(w.p, slog.New(slog.NewJSONHandler(&w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// logged is every record with this message, since the last call to forget.
func (w *world) logged(msg string) []map[string]any {
	w.t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(w.logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func TestATickDrainsTheOutboxAndSaysWhatItDid(t *testing.T) {
	w := newWorld(t)
	a := w.acme()
	waiting := w.outstanding()

	if err := w.job().run(t.Context(), offTheQuarter); err != nil {
		t.Fatalf("run: %v", err)
	}
	w.wantGraph(a.team.ID, slices.Clone(acmeNodes), slices.Clone(acmeEdges))

	said := w.logged("Graph drained")
	if len(said) != 1 || said[0]["projected"] != float64(waiting) || said[0]["failed"] != float64(0) || said[0]["level"] != "INFO" {
		t.Errorf("logged %v, want one line with %d projected", said, waiting)
	}
}

// 1,440 ticks a day, nearly all of them with nothing to do.
func TestATickWithNothingToDoSaysNothing(t *testing.T) {
	w := newWorld(t)
	w.acme()
	w.drained()
	w.logs.Reset()

	for _, tick := range []time.Time{onTheQuarter, offTheQuarter} {
		if err := w.job().run(t.Context(), tick); err != nil {
			t.Fatal(err)
		}
	}
	if w.logs.Len() != 0 {
		t.Errorf("an idle tick logged:\n%s", w.logs.String())
	}
}

// A failure is worth a line even when nothing was projected.
func TestATickThatOnlyFailedSaysSo(t *testing.T) {
	w := newWorld(t)
	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")
	w.down.Store(true)

	if err := w.job().run(t.Context(), offTheQuarter); err != nil {
		t.Fatal(err)
	}
	said := w.logged("Graph drained")
	if len(said) != 1 || said[0]["projected"] != float64(0) || said[0]["failed"] != float64(1) {
		t.Errorf("logged %v", said)
	}
}

// Another worker draining is the ordinary case with two replicas, not a
// failed workflow.
func TestATickWhileAnotherWorkerDrainsIsNotAnError(t *testing.T) {
	w := newWorld(t)
	w.acme()
	waiting := w.outstanding()

	_, err := w.store.WithAdvisoryLock(t.Context(), w.p.lockID, func(ctx context.Context) error {
		return w.job().run(ctx, offTheQuarter)
	})
	if err != nil {
		t.Errorf("run under another's lock: %v", err)
	}
	if w.outstanding() != waiting || len(w.logged("Graph drained")) != 0 {
		t.Errorf("a busy tick did or said something:\n%s", w.logs.String())
	}
}

// A drain that could not run fails its workflow, where it is seen.
func TestATickThatCannotDrainIsAnError(t *testing.T) {
	w := newWorld(t)
	w.acme()
	w.store.claim = errors.New("the database went away")

	err := w.job().run(t.Context(), onTheQuarter)
	if err == nil || !strings.Contains(err.Error(), "the database went away") {
		t.Errorf("run: err = %v", err)
	}
}

// --- the alarm ---

// stuck leaves one row in the outbox that fails every time, written this long
// ago.
func (w *world) stuck(age time.Duration) {
	w.t.Helper()

	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")
	w.down.Store(true)
	w.exec("UPDATE graph_outbox SET created_at = now() - make_interval(secs => $1)", age.Seconds())
}

// An outbox that stops shrinking is a projector that stopped. The age of the
// oldest row is the signal, never the count.
func TestAGraphLeftBehindIsReportedOnTheQuarterHour(t *testing.T) {
	w := newWorld(t)
	w.stuck(time.Hour)

	if err := w.job().run(t.Context(), onTheQuarter); err != nil {
		t.Fatalf("a graph that is behind failed the tick: %v", err)
	}
	said := w.logged("The graph is behind")
	if len(said) != 1 {
		t.Fatalf("logged %d alarms, want 1:\n%s", len(said), w.logs.String())
	}
	if said[0]["level"] != "ERROR" || said[0]["outstanding"] != float64(1) || said[0]["oldest"] == nil {
		t.Errorf("alarm = %v", said[0])
	}
	// The age is a duration in nanoseconds, rounded to the minute.
	if age, _ := said[0]["age"].(float64); time.Duration(age) != time.Hour {
		t.Errorf("age = %v, want 1h", time.Duration(age))
	}

	// Once a quarter of an hour, not once a minute: the row's own failure is
	// already logged every time it is tried.
	w.retryNow()
	w.must(w.job().run(t.Context(), offTheQuarter))
	if n := len(w.logged("The graph is behind")); n != 1 {
		t.Errorf("%d alarms after a tick off the quarter hour, want still 1", n)
	}

	// A quarter hour somewhere whose clock is thirty minutes off UTC's.
	india := time.FixedZone("IST", 5*3600+1800)
	w.retryNow()
	w.must(w.job().run(t.Context(), time.Date(2026, 9, 1, 16, 0, 0, 0, india)))
	if n := len(w.logged("The graph is behind")); n != 2 {
		t.Errorf("%d alarms after a second quarter-hour tick, want 2", n)
	}
}

func TestABacklogYoungerThanTheThresholdIsNotAnAlarm(t *testing.T) {
	w := newWorld(t)
	w.stuck(behindAfter - time.Minute)

	w.must(w.job().run(t.Context(), onTheQuarter))
	if n := len(w.logged("The graph is behind")); n != 0 {
		t.Errorf("a row a minute short of the threshold raised %d alarms", n)
	}

	w.exec("UPDATE graph_outbox SET created_at = now() - make_interval(secs => $1)", (behindAfter + time.Minute).Seconds())
	w.retryNow()
	w.must(w.job().run(t.Context(), onTheQuarter))
	if n := len(w.logged("The graph is behind")); n != 1 {
		t.Errorf("a row a minute past the threshold raised %d alarms", n)
	}
}

// The alarm does not depend on this worker having drained: a worker that is
// always turned away still reports a graph that is behind.
func TestABusyTickStillRaisesTheAlarm(t *testing.T) {
	w := newWorld(t)
	w.stuck(time.Hour)

	_, err := w.store.WithAdvisoryLock(t.Context(), w.p.lockID, func(ctx context.Context) error {
		return w.job().run(ctx, onTheQuarter)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(w.logged("The graph is behind")); n != 1 {
		t.Errorf("%d alarms from a busy tick, want 1", n)
	}
}

func TestABacklogThatCannotBeReadIsLoggedAndTheTickStands(t *testing.T) {
	w := newWorld(t)
	w.stuck(time.Hour)
	w.store.backlog = errors.New("the database went away")

	if err := w.job().run(t.Context(), onTheQuarter); err != nil {
		t.Errorf("run: %v", err)
	}
	said := w.logged("Reading the graph backlog")
	if len(said) != 1 || said[0]["error"] != "the database went away" || said[0]["level"] != "ERROR" {
		t.Errorf("logged %v", said)
	}
	if n := len(w.logged("The graph is behind")); n != 0 {
		t.Error("an alarm was raised from a backlog that could not be read")
	}
}

func TestBacklogCountsEveryRowAndFindsTheOldest(t *testing.T) {
	w := newWorld(t)

	outstanding, oldest, err := w.p.Backlog(t.Context())
	if err != nil || outstanding != 0 || !oldest.IsZero() {
		t.Errorf("an empty outbox: %d, %v, %v", outstanding, oldest, err)
	}

	team := w.team("acme")
	w.skill(team, "pdf", "Extract text")
	w.skill(team, "docx", "Write Word documents")
	w.skill(team, "xlsx", "Read spreadsheets")
	w.exec(`UPDATE graph_outbox SET created_at = now() - interval '2 hours'
		WHERE id = (SELECT id FROM graph_outbox ORDER BY id OFFSET 1 LIMIT 1)`)

	outstanding, oldest, err = w.p.Backlog(t.Context())
	if err != nil || outstanding != 3 {
		t.Fatalf("Backlog = %d, %v", outstanding, err)
	}
	if age := time.Since(oldest); age < 2*time.Hour-time.Minute || age > 2*time.Hour+time.Minute {
		t.Errorf("the oldest row is %v old, want two hours", age)
	}

	w.store.backlog = errors.New("the database went away")
	if _, _, err := w.p.Backlog(t.Context()); err == nil {
		t.Error("a backlog that could not be read returned no error")
	}
}
