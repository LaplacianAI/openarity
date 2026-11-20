package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

const (
	// Long enough for a worker to launch and dequeue on a loaded CI machine.
	pickUpTimeout = 30 * time.Second
	// How long a workflow of another version is watched not being dequeued:
	// several of DBOS's queue polls.
	ignoredFor = 5 * time.Second
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// testDB is an empty database of the test's own, so its workers see no
// queue, workflow or version but the test's, and it is dropped at the end.
// The version override is cleared, as a worker would run under it.
func testDB(t *testing.T) string {
	t.Helper()
	t.Setenv("DBOS__APPVERSION", "")
	return storetest.Empty(t)
}

// blockingJob declares one queue and one workflow on it whose step holds
// open until it is let go, so a worker can be stopped in the middle of it.
type blockingJob struct {
	queue    string
	started  chan string
	release  chan struct{}
	finished chan string
}

func newBlockingJob(t *testing.T) *blockingJob {
	return &blockingJob{
		queue:    fmt.Sprintf("test-%s", t.Name()),
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
		select {
		case j.started <- in:
		case <-sctx.Done():
			return "", sctx.Err()
		}
		select {
		case <-sctx.Done():
			return "", sctx.Err()
		case <-j.release:
		}
		select {
		case j.finished <- in:
			return in, nil
		case <-sctx.Done():
			return "", sctx.Err()
		}
	})
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

// launchBuild registers a version as another build's launch would, then
// shuts down, before any workflow exists for it to take.
func launchBuild(t *testing.T, dsn, version string) {
	t.Helper()
	c, err := dbos.NewContext(context.Background(), dbos.Config{
		AppName: AppName, ApplicationVersion: version, DatabaseURL: dsn,
		Logger: discardLogger(), ExecutorID: version,
	})
	if err != nil {
		t.Fatalf("the %s build's context: %v", version, err)
	}
	if err := c.Launch(); err != nil {
		t.Fatalf("launch the %s build: %v", version, err)
	}
	if err := dbos.Shutdown(c, 5*time.Second); err != nil {
		t.Errorf("shut the %s build down: %v", version, err)
	}
}

// testClient enqueues as the API does. Not t.Context(): that is cancelled
// before the cleanup that shuts the client down runs.
func testClient(t *testing.T, dsn string) dbos.Client {
	t.Helper()
	c, err := dbos.NewClient(context.Background(), dbos.ClientConfig{
		DatabaseURL: dsn, AppName: AppName, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("dbos client: %v", err)
	}
	t.Cleanup(func() {
		if err := dbos.Shutdown(c, 5*time.Second); err != nil {
			t.Errorf("shut the client down: %v", err)
		}
	})
	return c
}

func enqueue(t *testing.T, c dbos.Client, job *blockingJob, id string, opts ...dbos.EnqueueOption) {
	t.Helper()
	opts = append([]dbos.EnqueueOption{dbos.WithEnqueueWorkflowID(id)}, opts...)
	if _, err := dbos.Enqueue[string](c, job.queue, job.queue, id, opts...); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

func awaitStart(t *testing.T, c dbos.Client, job *blockingJob, id string) {
	t.Helper()
	select {
	case <-job.started:
	case <-time.After(pickUpTimeout):
		t.Fatalf("no worker started %s; it is %s", id, statusOf(t, c, id))
	}
}

func awaitFinish(t *testing.T, c dbos.Client, job *blockingJob, id string) {
	t.Helper()
	select {
	case got := <-job.finished:
		if got != id {
			t.Fatalf("the worker finished %s, want %s", got, id)
		}
	case <-time.After(pickUpTimeout):
		t.Fatalf("no worker finished %s; it is %s", id, statusOf(t, c, id))
	}
}

func latestVersion(t *testing.T, c dbos.Client) string {
	t.Helper()
	v, err := dbos.GetLatestApplicationVersion(c)
	if err != nil {
		t.Fatalf("the latest version: %v", err)
	}
	return v.Name
}

// A deploy stops the worker with a signal in the middle of a run. The run's
// workflow must survive that as PENDING, not CANCELLED, and the next worker
// must take it up: staging lost every long run to a deploy before this.
func TestARunStoppedByASignalIsResumedByTheNextWorker(t *testing.T) {
	dsn := testDB(t)
	job := newBlockingJob(t)
	client := testClient(t, dsn)

	stop := startWorker(t, dsn, job)
	enqueue(t, client, job, "run")
	awaitStart(t, client, job, "run")
	stop()

	if got := statusOf(t, client, "run"); got != dbos.WorkflowStatusPending {
		t.Fatalf("after the signal the workflow is %s, want PENDING: a stop must not cancel it", got)
	}

	// What restartRun in internal/runtime does for a run the sweep took over.
	startWorker(t, dsn, job)
	if _, err := dbos.ResumeWorkflow[string](client, "run", dbos.WithResumeQueue(job.queue)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	awaitStart(t, client, job, "run")
	close(job.release)
	awaitFinish(t, client, job, "run")
}

// A worker dequeues only workflows of its own version. One stamped with the
// fixed version, as a run enqueued by any earlier build is, is taken up; one
// of another version is left alone. Both workers in the test above run one
// binary, so only this sees a version that is not the fixed one.
func TestAWorkerTakesUpWhatAnyBuildOfThisVersionQueued(t *testing.T) {
	dsn := testDB(t)
	job := newBlockingJob(t)
	client := testClient(t, dsn)
	close(job.release)
	startWorker(t, dsn, job)

	enqueue(t, client, job, "other", dbos.WithEnqueueApplicationVersion("a-build-gone-by"))
	enqueue(t, client, job, "ours", dbos.WithEnqueueApplicationVersion(appVersion))

	awaitFinish(t, client, job, "ours")
	select {
	case got := <-job.finished:
		t.Errorf("the worker ran %s, a workflow of another version", got)
	case <-time.After(ignoredFor):
	}
	if got := statusOf(t, client, "other"); got != dbos.WorkflowStatusEnqueued {
		t.Errorf("the other version's workflow is %s, want it still ENQUEUED", got)
	}
}

// The API enqueues a run with no version, which DBOS hands only to a worker
// of the latest version. Another build launched since this version was first
// registered, a rollback say, would otherwise keep that for good, and every
// new run would sit queued.
func TestAWorkerTakesUpTheAPIsRunsAfterAnotherBuildLaunched(t *testing.T) {
	dsn := testDB(t)
	job := newBlockingJob(t)
	client := testClient(t, dsn)
	launchBuild(t, dsn, appVersion)
	launchBuild(t, dsn, "another-build")
	if got := latestVersion(t, client); got != "another-build" {
		t.Fatalf("the latest version is %s before the worker starts, want another-build", got)
	}

	close(job.release)
	startWorker(t, dsn, job)
	enqueue(t, client, job, "run")
	awaitFinish(t, client, job, "run")
}

// DBOS__APPVERSION overrides the config's version, so the version a worker
// makes the latest must be the one it runs under, not appVersion.
func TestAWorkerUnderAnOverriddenVersionStillTakesUpTheAPIsRuns(t *testing.T) {
	dsn := testDB(t)
	job := newBlockingJob(t)
	client := testClient(t, dsn)
	launchBuild(t, dsn, "env-v")
	launchBuild(t, dsn, "another-build")

	t.Setenv("DBOS__APPVERSION", "env-v")
	close(job.release)
	startWorker(t, dsn, job)
	enqueue(t, client, job, "run")
	awaitFinish(t, client, job, "run")
	if got := latestVersion(t, client); got != "env-v" {
		t.Errorf("the latest version is %s, want env-v, the one the worker runs under", got)
	}
}

// A signal while the worker starts stops it cleanly rather than leaving it
// running: DBOS's context no longer hears the signal, so Run must.
func TestASignalDuringStartupStopsTheWorker(t *testing.T) {
	dsn := testDB(t)
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
