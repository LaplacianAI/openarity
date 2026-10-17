package runtime

import (
	"context"
	"fmt"
	"hash/crc32"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// --- the schedule ---

// dbosParser is the parser DBOS builds, seconds first, so the constant is
// read here by the thing that will read it in the worker.
func dbosParser() cron.Parser {
	return cron.NewParser(cron.Second | cron.Minute | cron.Hour |
		cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

func TestTheSweepCronParsesWithTheParserDBOSBuilds(t *testing.T) {
	t.Parallel()

	schedule, err := dbosParser().Parse(sweepCron)
	if err != nil {
		t.Fatalf("sweepCron = %q does not parse: %v", sweepCron, err)
	}
	first := schedule.Next(time.Date(2026, 10, 1, 3, 7, 12, 0, time.UTC))
	if gap := schedule.Next(first).Sub(first); gap != time.Minute || first.Second() != 0 {
		t.Errorf("ticks at %v, %v apart; want the top of every minute", first, gap)
	}
}

func TestAFiveFieldCronIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := dbosParser().Parse("* * * * *"); err == nil {
		t.Fatal("the five-field form parsed, so the six-field constant is not load-bearing")
	}
}

// A missed minute has no work of its own: the next sweep finds the same rows.
func TestTheSweepSpecIsWhatItClaims(t *testing.T) {
	t.Parallel()

	got := (&Job{}).schedule()
	want := dbos.ScheduleSpec{ScheduleName: sweepSchedule, Schedule: sweepCron, WorkflowName: sweepWorkflow}
	if got.ScheduleName != want.ScheduleName || got.Schedule != want.Schedule ||
		got.WorkflowName != want.WorkflowName || got.AutomaticBackfill {
		t.Errorf("schedule = %+v, want %+v without backfill", got, want)
	}
}

// A sweep that lets runs pile up behind a lost one is a sweep slower than a
// run's takeover: stale must be well past a heartbeat, and well under the
// time a person would wait.
func TestTheSweepWaitsLongerThanAHeartbeatButNotLongerThanItsGiveUp(t *testing.T) {
	t.Parallel()

	if staleAfter < 4*heartbeatEvery || heartbeatLostAfter >= staleAfter {
		t.Errorf("heartbeat %v, a worker gives up after %v, the sweep takes over after %v",
			heartbeatEvery, heartbeatLostAfter, staleAfter)
	}
}

// --- a worker, for real ---

// dbosSchema is a DBOS system schema of the test's own, short enough for
// Postgres's 63 bytes whatever the test is called.
func dbosSchema(t *testing.T) string {
	return fmt.Sprintf("dbos_test_%08x", crc32.ChecksumIEEE([]byte(t.Name())))
}

func (f *execFixture) dbosConfig(t *testing.T, executor string) dbos.Config {
	return dbos.Config{
		AppName: "openarity-brain-test", DatabaseURL: f.dsn, DatabaseSchema: dbosSchema(t),
		ExecutorID: executor, Logger: quietLogger(),
	}
}

// worker launches a DBOS runtime that runs the job, and shuts it down with
// the test. The sweep is not scheduled: tests call sweepOnce when they mean it.
func (f *execFixture) worker(t *testing.T, executor string) (dbos.Context, *Job, func()) {
	t.Helper()

	d, err := dbos.NewContext(context.WithoutCancel(t.Context()), f.dbosConfig(t, executor))
	if err != nil {
		t.Fatalf("dbos.NewContext: %v", err)
	}
	job := RunJob(f.exec, f.store, quietLogger())
	if _, err := job.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := d.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	var once sync.Once
	stop := func() { once.Do(func() { _ = dbos.Shutdown(d, 5*time.Second) }) }
	t.Cleanup(stop)
	return d, job, stop
}

func (f *execFixture) dropDBOS(t *testing.T) {
	t.Helper()

	schema := dbosSchema(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()
		if _, err := f.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop %s: %v", schema, err)
		}
	})
}

func newJobFixture(t *testing.T, model *scripted) *execFixture {
	t.Helper()

	f := newExecFixture(t, model)
	f.dropDBOS(t)
	return f
}

// waitForStatus polls the run until it reaches status, failing after 20s.
func (f *execFixture) waitForStatus(t *testing.T, id uuid.UUID, status string) db.Run {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err := f.store.GetRun(t.Context(), id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if r.Status == status {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s after 20s, want %s (error %s)", id, r.Status, status, deref(r.Error))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- the queue ---

func TestAnEnqueuedRunIsExecutedWithinSeconds(t *testing.T) {
	f := newJobFixture(t, newScripted(answer("42", 1, 1)))
	f.worker(t, "w1")
	client, err := dbos.NewClient(t.Context(), dbos.ClientConfig{
		DatabaseURL: f.dsn, AppName: "openarity-brain-test", DatabaseSchema: dbosSchema(t),
	})
	if err != nil {
		t.Fatalf("dbos.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = dbos.Shutdown(client, 5*time.Second) })

	start := time.Now()
	if err := NewEnqueuer(client).Enqueue(t.Context(), f.run); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	r := f.waitForStatus(t, f.run, "succeeded")
	if took := time.Since(start); took > 10*time.Second || deref(r.Output) != "42" {
		t.Errorf("succeeded after %v with %q", took, deref(r.Output))
	}
	if deref(r.Worker) != "w1" {
		t.Errorf("run by %s, want the worker's own executor id", deref(r.Worker))
	}
}

// The API's enqueue, a retried enqueue and the sweep's enqueue all name the
// workflow by the run, so none of them can start it twice.
func TestEnqueuingARunTwiceRunsItOnce(t *testing.T) {
	f := newJobFixture(t, newScripted(answer("42", 1, 1)))
	d, _, _ := f.worker(t, "w1")

	for range 2 {
		if err := enqueue(d, f.run); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	f.waitForStatus(t, f.run, "succeeded")
	if err := enqueue(d, f.run); err != nil {
		t.Fatalf("enqueue after it finished: %v", err)
	}
	time.Sleep(2 * time.Second)

	if n := len(f.model.requests()); n != 1 {
		t.Errorf("the model was called %d times, want once", n)
	}
	started := 0
	for _, e := range f.events(t) {
		if e.Kind == "started" {
			started++
		}
	}
	if started != 1 {
		t.Errorf("%d started events, want 1", started)
	}
}

// The API commits the row and enqueues after; a crash between the two leaves
// a run nothing will pick up but the sweep.
func TestTheSweepStartsARunWhoseEnqueueWasLost(t *testing.T) {
	f := newJobFixture(t, newScripted(answer("42", 1, 1)))
	_, job, _ := f.worker(t, "w1")
	f.mustExec(t, `UPDATE runs SET created_at = now() - interval '1 minute' WHERE id = $1`, f.run)

	if err := job.sweepOnce(t.Context()); err != nil {
		t.Fatalf("sweepOnce: %v", err)
	}

	f.waitForStatus(t, f.run, "succeeded")
}

func TestTheSweepLeavesARecentRunToItsEnqueue(t *testing.T) {
	f := newJobFixture(t, newScripted(answer("42", 1, 1)))
	_, job, _ := f.worker(t, "w1")

	if err := job.sweepOnce(t.Context()); err != nil {
		t.Fatalf("sweepOnce: %v", err)
	}
	time.Sleep(2 * time.Second)

	if r := f.row(t); r.Status != "queued" {
		t.Errorf("status = %s; a run created a moment ago was started by the sweep", r.Status)
	}
}

// --- a worker that dies ---

// interruptedScript is a run that calls a tool, then is mid-call to the model
// when its worker stops, and answers when resumed.
func interruptedScript() *scripted {
	return newScripted(callTool("c1", "nope", `{"q":1}`), blocked(), answer("done", 1, 1))
}

// killMidRun starts the run on worker a, waits until it is inside its second
// model call, and shuts a down. The run is left running with a's heartbeat,
// which is then aged past the takeover.
func (f *execFixture) killMidRun(t *testing.T) {
	t.Helper()

	a, _, stopA := f.worker(t, "worker-a")
	if err := enqueue(a, f.run); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	for range 2 {
		if !f.model.waitForACall() {
			t.Fatal("the run never reached the model")
		}
	}
	stopA()

	r := f.row(t)
	if r.Status != "running" || deref(r.Worker) != "worker-a" {
		t.Fatalf("after worker-a stopped: status %s, worker %s; want running on worker-a", r.Status, deref(r.Worker))
	}
	// A heartbeat this stale means the run started, so was created, longer ago
	// still: a run is never stale and recent.
	f.mustExec(t, `UPDATE runs SET heartbeat_at = now() - interval '2 minutes',
	    created_at = now() - interval '5 minutes', started_at = now() - interval '5 minutes' WHERE id = $1`, f.run)
}

func TestTheSweepResumesAStalledRun(t *testing.T) {
	f := newJobFixture(t, interruptedScript())
	f.killMidRun(t)

	bd, b, _ := f.worker(t, "worker-b")
	if err := b.sweepOnce(t.Context()); err != nil {
		t.Fatalf("worker-b's sweep: %v", err)
	}

	// Resumed onto the run queue, not DBOS's internal one, which has no cap:
	// a takeover must not be a way past four runs a worker.
	st, err := dbos.ListWorkflows(bd, dbos.WithFilterWorkflowIDs(f.run.String()))
	if err != nil || len(st) != 1 || st[0].QueueName != RunQueue {
		t.Errorf("the resumed workflow is on %+v, %v; want %s", st, err, RunQueue)
	}

	r := f.waitForStatus(t, f.run, "succeeded")
	if deref(r.Output) != "done" || deref(r.Worker) != "worker-b" {
		t.Errorf("output %q on %s, want done on worker-b", deref(r.Output), deref(r.Worker))
	}
	got := kinds(f.events(t))
	resumed := 0
	for _, k := range got {
		if k == "resumed" {
			resumed++
		}
	}
	if resumed != 1 || got[0] != "started" {
		t.Errorf("events = %v, want started by worker-a and one resumed", got)
	}
}

type runRecord struct {
	status, output string
	calls          []string
}

func recordOf(t *testing.T, f *execFixture) runRecord {
	t.Helper()

	r := f.row(t)
	out := runRecord{status: r.Status, output: deref(r.Output)}
	for _, e := range f.events(t) {
		if e.Kind == "tool_call" {
			p := payload[map[string]any](t, e)
			out.calls = append(out.calls, fmt.Sprint(p["name"], p["arguments"]))
		}
	}
	return out
}

// The spec's test of a takeover: what a reader of the record sees does not
// depend on whether a worker died under the run.
func TestAResumedRunEndsAsAnUninterruptedOne(t *testing.T) {
	var resumed, straight runRecord

	t.Run("resumed", func(t *testing.T) {
		f := newJobFixture(t, interruptedScript())
		f.killMidRun(t)
		_, b, _ := f.worker(t, "worker-b")
		if err := b.sweepOnce(t.Context()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		f.waitForStatus(t, f.run, "succeeded")
		resumed = recordOf(t, f)
	})
	t.Run("straight", func(t *testing.T) {
		f := newJobFixture(t, newScripted(callTool("c1", "nope", `{"q":1}`), answer("done", 1, 1)))
		d, _, _ := f.worker(t, "worker-a")
		if err := enqueue(d, f.run); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		f.waitForStatus(t, f.run, "succeeded")
		straight = recordOf(t, f)
	})

	if resumed.status != straight.status || resumed.output != straight.output || !slices.Equal(resumed.calls, straight.calls) {
		t.Errorf("resumed %+v, uninterrupted %+v", resumed, straight)
	}
}

// --- capacity ---

// gate holds every call until released, and counts how many it holds at once.
type gate struct {
	mu       sync.Mutex
	inFlight int
	most     int
	release  chan struct{}
}

func (g *gate) Complete(ctx context.Context, _ agent.Request) (agent.Response, error) {
	g.mu.Lock()
	g.inFlight++
	g.most = max(g.most, g.inFlight)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()

	select {
	case <-g.release:
	case <-ctx.Done():
		return agent.Response{}, ctx.Err()
	}
	return answer("ok", 1, 1).resp, nil
}

func (*gate) Stream(context.Context, agent.Request) (agent.Stream, error) {
	return nil, fmt.Errorf("these runs do not stream")
}

func (g *gate) counts() (now, most int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight, g.most
}

func TestTheWorkerRunsAtMostFourRunsAtOnce(t *testing.T) {
	f := newJobFixture(t, newScripted())
	g := &gate{release: make(chan struct{})}
	f.exec = f.executor(t, func(agent.Endpoint) (agent.ModelClient, error) { return g, nil },
		agent.Endpoint{BaseURL: "http://model.test"})
	d, _, _ := f.worker(t, "w1")

	runs := []uuid.UUID{f.run}
	for range runsPerWorker + 2 {
		runs = append(runs, f.anotherRun(t))
	}
	for _, id := range runs {
		if err := enqueue(d, id); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for now, _ := g.counts(); now < runsPerWorker; now, _ = g.counts() {
		if time.Now().After(deadline) {
			t.Fatalf("only %d runs reached the model", now)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	if _, most := g.counts(); most != runsPerWorker {
		t.Errorf("%d runs at once, want %d", most, runsPerWorker)
	}

	close(g.release)
	for _, id := range runs {
		f.waitForStatus(t, id, "succeeded")
	}
}

func (f *execFixture) anotherRun(t *testing.T) uuid.UUID {
	t.Helper()

	var session uuid.UUID
	if err := f.raw.QueryRow(t.Context(),
		`SELECT session_id FROM runs WHERE id = $1`, f.run).Scan(&session); err != nil {
		t.Fatalf("session: %v", err)
	}
	r, err := f.store.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: session, AgentID: f.agent, Input: runInput,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r.ID
}
