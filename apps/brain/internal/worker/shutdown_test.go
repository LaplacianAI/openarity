package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// Long enough for a worker to launch and dequeue on a loaded CI machine.
	pickUpTimeout = 30 * time.Second
	// How long a workflow of another version is watched not being dequeued:
	// several of DBOS's queue polls.
	ignoredFor = 5 * time.Second
)

// blockingJob declares one queue and one workflow on it, named after the
// test, whose step holds open until it is let go, so a worker can be stopped
// in the middle of it. Only a worker running this job dequeues that queue, so
// tests sharing a database never take each other's workflows.
type blockingJob struct {
	queue    string
	started  chan string
	release  chan struct{}
	finished chan string
}

func newBlockingJob(t *testing.T) *blockingJob {
	return &blockingJob{
		queue:    fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano()),
		started:  make(chan string, 4),
		release:  make(chan struct{}),
		finished: make(chan string, 4),
	}
}

func (j *blockingJob) Name() string { return "blocking" }

func (j *blockingJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if _, err := dbos.RegisterQueue(d, j.queue); err != nil {
		return nil, err
	}
	dbos.RegisterWorkflow(d, j.run, dbos.WithWorkflowName(j.queue))
	return nil, nil
}

func (j *blockingJob) run(ctx dbos.Context, in string) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		j.started <- in
		select {
		case <-sctx.Done():
			return "", sctx.Err()
		case <-j.release:
			j.finished <- in
			return in, nil
		}
	})
}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

// startWorker runs a worker until the returned stop is called, or the test
// ends, whichever is first.
func startWorker(t *testing.T, dsn string, job *blockingJob) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(dsn, discardLogger(), job).Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("the worker stopped with %v", err)
				}
			case <-time.After(shutdownGrace + 10*time.Second):
				t.Error("the worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// testClient enqueues as the API does, and deletes what the test made.
func testClient(t *testing.T, dsn string, ids ...string) dbos.Client {
	t.Helper()
	c, err := dbos.NewClient(t.Context(), dbos.ClientConfig{
		DatabaseURL: dsn, AppName: AppName, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("dbos client: %v", err)
	}
	t.Cleanup(func() {
		_ = dbos.DeleteWorkflows(c, ids)
		_ = dbos.Shutdown(c, 5*time.Second)
	})
	return c
}

func awaitStart(t *testing.T, c dbos.Client, job *blockingJob, id string) {
	t.Helper()
	select {
	case <-job.started:
	case <-time.After(pickUpTimeout):
		t.Fatalf("no worker started %s; it is %s", id, statusOf(t, c, id))
	}
}

// A deploy stops the worker with a signal in the middle of a run. The run's
// workflow must survive that as PENDING, not CANCELLED, and the next worker
// must take it up: staging lost every long run to a deploy before this.
func TestARunStoppedByASignalIsResumedByTheNextWorker(t *testing.T) {
	dsn := testDSN(t)
	job := newBlockingJob(t)
	id := job.queue + "-wf"
	client := testClient(t, dsn, id)

	stop := startWorker(t, dsn, job)
	if _, err := dbos.Enqueue[string](client, job.queue, job.queue, "x",
		dbos.WithEnqueueWorkflowID(id)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	awaitStart(t, client, job, id)
	stop()

	if got := statusOf(t, client, id); got != dbos.WorkflowStatusPending {
		t.Fatalf("after the signal the workflow is %s, want PENDING: a stop must not cancel it", got)
	}

	// What restartRun in internal/runtime does for a run the sweep took over.
	startWorker(t, dsn, job)
	if _, err := dbos.ResumeWorkflow[string](client, id, dbos.WithResumeQueue(job.queue)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	awaitStart(t, client, job, id)
	close(job.release)
	if got := <-job.finished; got != "x" {
		t.Errorf("the workflow finished with %q, want x", got)
	}
}

// A worker dequeues only workflows of its own version. One stamped with the
// fixed version, as a run enqueued by any earlier build is, is taken up; one
// of another version is left alone. Both workers in the test above run one
// binary, so only this sees a version that is not the fixed one.
func TestAWorkerTakesUpWhatAnyBuildOfThisVersionQueued(t *testing.T) {
	dsn := testDSN(t)
	job := newBlockingJob(t)
	other, ours := job.queue+"-other", job.queue+"-ours"
	client := testClient(t, dsn, other, ours)
	close(job.release)
	startWorker(t, dsn, job)

	if _, err := dbos.Enqueue[string](client, job.queue, job.queue, other,
		dbos.WithEnqueueWorkflowID(other),
		dbos.WithEnqueueApplicationVersion("a-build-gone-by")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := dbos.Enqueue[string](client, job.queue, job.queue, ours,
		dbos.WithEnqueueWorkflowID(ours),
		dbos.WithEnqueueApplicationVersion(appVersion)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case got := <-job.finished:
		if got != ours {
			t.Fatalf("the worker ran %s, a workflow of another version", got)
		}
	case <-time.After(pickUpTimeout):
		t.Fatalf("the worker never took up a workflow of its own version; it is %s", statusOf(t, client, ours))
	}
	select {
	case got := <-job.finished:
		t.Errorf("the worker ran %s, a workflow of another version", got)
	case <-time.After(ignoredFor):
	}
	if got := statusOf(t, client, other); got != dbos.WorkflowStatusEnqueued {
		t.Errorf("the other version's workflow is %s, want it still ENQUEUED", got)
	}
}

// The API enqueues a run with no version, which DBOS hands only to a worker
// of the latest version. Another build launched after this one, a rollback
// say, would otherwise take that from it, and every new run would sit queued.
func TestAWorkerTakesUpTheAPIsRunsAfterAnotherBuildLaunched(t *testing.T) {
	dsn := testDSN(t)
	job := newBlockingJob(t)
	id := job.queue + "-wf"
	client := testClient(t, dsn, id)

	other, err := dbos.NewContext(context.Background(), dbos.Config{
		AppName: AppName, ApplicationVersion: job.queue + "-another-build",
		DatabaseURL: dsn, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("another build's context: %v", err)
	}
	if err := other.Launch(); err != nil {
		t.Fatalf("launch another build: %v", err)
	}
	_ = dbos.Shutdown(other, 5*time.Second)

	close(job.release)
	startWorker(t, dsn, job)
	if _, err := dbos.Enqueue[string](client, job.queue, job.queue, "x",
		dbos.WithEnqueueWorkflowID(id)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-job.finished:
	case <-time.After(pickUpTimeout):
		t.Fatalf("the worker never took up the API's run; it is %s", statusOf(t, client, id))
	}
}

// A signal while the worker starts stops it cleanly rather than leaving it
// running: DBOS's context no longer hears the signal, so Run must.
func TestASignalDuringStartupStopsTheWorker(t *testing.T) {
	dsn := testDSN(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- New(dsn, discardLogger(), newBlockingJob(t)).Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil: a stop is not a failure", err)
		}
	case <-time.After(pickUpTimeout):
		t.Fatal("the worker kept running after a signal during startup")
	}
}

func statusOf(t *testing.T, c dbos.Client, id string) dbos.WorkflowStatusType {
	t.Helper()
	h, err := dbos.RetrieveWorkflow[string](c, id)
	if err != nil {
		t.Fatalf("retrieve %s: %v", id, err)
	}
	s, err := h.GetStatus()
	if err != nil {
		t.Fatalf("status of %s: %v", id, err)
	}
	return s.Status
}
