package worker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// blockingJob declares one queued workflow that holds a step open until it is
// let go, so a worker can be stopped in the middle of it.
type blockingJob struct {
	queue    string
	started  chan struct{}
	release  chan struct{}
	finished chan string
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
		case j.started <- struct{}{}:
		default:
		}
		select {
		case <-sctx.Done():
			return "", sctx.Err()
		case <-j.release:
			j.finished <- in
			return in, nil
		}
	})
}

func startWorker(t *testing.T, dsn string, job *blockingJob) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- New(dsn, discardLogger(), job).Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the worker stopped with %v", err)
			}
		case <-time.After(shutdownGrace + 10*time.Second):
			t.Fatal("the worker did not stop")
		}
	}
}

// A deploy stops the worker with a signal in the middle of a run. The run's
// workflow must survive that as PENDING, not CANCELLED, and the next worker
// must take it up: staging lost every long run to a deploy before this.
func TestARunStoppedByASignalIsResumedByTheNextWorker(t *testing.T) {
	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	job := &blockingJob{
		queue:    fmt.Sprintf("test-shutdown-%d", time.Now().UnixNano()),
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		finished: make(chan string, 1),
	}
	id := job.queue + "-wf"

	client, err := dbos.NewClient(t.Context(), dbos.ClientConfig{
		DatabaseURL: dsn, AppName: AppName, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("dbos client: %v", err)
	}
	t.Cleanup(func() { _ = dbos.Shutdown(client, 5*time.Second) })

	stop := startWorker(t, dsn, job)
	if _, err := dbos.Enqueue[string](client, job.queue, job.queue, "x",
		dbos.WithEnqueueWorkflowID(id)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-job.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first worker never started the workflow")
	}
	stop()

	if got := statusOf(t, client, id); got != dbos.WorkflowStatusPending {
		t.Fatalf("after the signal the workflow is %s, want PENDING: a stop must not cancel it", got)
	}

	// What the run sweep does with a run whose worker stopped heartbeating.
	stop = startWorker(t, dsn, job)
	defer stop()
	if _, err := dbos.ResumeWorkflow[string](client, id, dbos.WithResumeQueue(job.queue)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	select {
	case <-job.started:
	case <-time.After(30 * time.Second):
		t.Fatalf("the next worker never took the workflow up; it is %s", statusOf(t, client, id))
	}
	close(job.release)
	if got := <-job.finished; got != "x" {
		t.Errorf("the workflow finished with %q, want x", got)
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
