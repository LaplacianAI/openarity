package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	RunWorkflow   = "openarity.runtime.run"
	RunQueue      = "openarity-runs"
	runsPerWorker = 4

	sweepWorkflow  = "openarity.runtime.sweep"
	sweepSchedule  = "runtime-sweep"
	sweepCron      = "0 * * * * *"
	unstartedAfter = 30 * time.Second
	staleAfter     = 90 * time.Second
)

type SweepStore interface {
	ListUnstartedRuns(ctx context.Context, olderThanSeconds float64) ([]db.ListUnstartedRunsRow, error)
	TakeOverStaleRuns(ctx context.Context, staleSeconds float64) ([]uuid.UUID, error)
}

type Job struct {
	executor  *Executor
	store     SweepStore
	logger    *slog.Logger
	dbos      dbos.Context
	restart   func(uuid.UUID, int32) error
	kick      func(dbos.Client, uuid.UUID) error
	kickEvent func(dbos.Client, uuid.UUID, int32) error
}

func RunJob(e *Executor, s SweepStore, logger *slog.Logger) *Job {
	j := &Job{executor: e, store: s, logger: logger}
	j.restart = j.restartRun
	return j
}

// SendsReplies has a channel run's reply sent as soon as the run finishes,
// through kick, and each step it tells as soon as it is told, through
// kickEvent. Without them replies wait for their sweep.
func (j *Job) SendsReplies(kick func(dbos.Client, uuid.UUID) error, kickEvent func(dbos.Client, uuid.UUID, int32) error) *Job {
	j.kick, j.kickEvent = kick, kickEvent
	return j
}

func (*Job) Name() string { return "runtime" }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.executor == nil || j.store == nil {
		return nil, errors.New("the runtime job has no executor or no store, so every run it dequeued would fail")
	}

	if _, err := dbos.RegisterQueue(d, RunQueue, dbos.WithWorkerConcurrency(runsPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the run queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.run, dbos.WithWorkflowName(RunWorkflow))
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(sweepWorkflow))
	j.dbos = d
	j.executor.enqueue = func(id uuid.UUID, attempt int32) error { return enqueue(d, id, attempt) }
	if j.kick != nil {
		j.executor.kickReply = func(id uuid.UUID) error { return j.kick(d, id) }
	}
	if j.kickEvent != nil {
		j.executor.kickEvent = func(id uuid.UUID, seq int32) error { return j.kickEvent(d, id, seq) }
	}

	return []dbos.ScheduleSpec{j.schedule()}, nil
}

func (*Job) schedule() dbos.ScheduleSpec {
	return dbos.ScheduleSpec{
		ScheduleName:      sweepSchedule,
		Schedule:          sweepCron,
		WorkflowName:      sweepWorkflow,
		AutomaticBackfill: false,
	}
}

func (j *Job) run(ctx dbos.Context, runID string) (string, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return "", fmt.Errorf("the run id %q is not a uuid: %w", runID, err)
	}
	worker := dbos.GetExecutorID(ctx)

	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return runID, j.executor.Execute(sctx, id, worker)
	})
}

func (j *Job) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepOnce(sctx)
	})
}

func (j *Job) sweepOnce(ctx context.Context) error {
	taken, err := j.store.TakeOverStaleRuns(ctx, staleAfter.Seconds())
	if err != nil {
		return fmt.Errorf("taking over stale runs: %w", err)
	}
	if len(taken) > 0 {
		j.logger.WarnContext(ctx, "Took over runs whose worker stopped heartbeating", "runs", len(taken))
	}

	queued, err := j.store.ListUnstartedRuns(ctx, unstartedAfter.Seconds())
	if err != nil {
		return fmt.Errorf("listing unstarted runs: %w", err)
	}

	var failed []error
	for _, r := range queued {
		if err := j.restart(r.ID, r.Attempt); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (j *Job) restartRun(id uuid.UUID, attempt int32) error {
	if err := enqueue(j.dbos, id, attempt); err != nil {
		return err
	}
	if _, err := dbos.ResumeWorkflow[string](j.dbos, workflowID(id, attempt), dbos.WithResumeQueue(RunQueue)); err != nil {
		return fmt.Errorf("resuming run %s: %w", id, err)
	}
	return nil
}

// workflowID is the DBOS workflow a run's attempt runs as. A finished
// workflow's id is an idempotency key and never runs again, so a run resumed
// after waiting for an approval is a workflow of its own: the run's id for
// its first attempt, and the id and the attempt after.
func workflowID(id uuid.UUID, attempt int32) string {
	if attempt == 0 {
		return id.String()
	}
	return fmt.Sprintf("%s:%d", id, attempt)
}

func enqueue(c dbos.Client, id uuid.UUID, attempt int32) error {
	_, err := dbos.Enqueue[string](c, RunQueue, RunWorkflow, id.String(), dbos.WithEnqueueWorkflowID(workflowID(id, attempt)))
	if err != nil {
		return fmt.Errorf("enqueuing run %s: %w", id, err)
	}
	return nil
}

type Enqueuer struct {
	client dbos.Client
}

func NewEnqueuer(c dbos.Client) *Enqueuer { return &Enqueuer{client: c} }

func (q *Enqueuer) Enqueue(_ context.Context, runID uuid.UUID) error {
	return enqueue(q.client, runID, 0)
}

// Resume hands a run that waited back to the workers, as its next attempt.
func (q *Enqueuer) Resume(_ context.Context, runID uuid.UUID, attempt int32) error {
	return enqueue(q.client, runID, attempt)
}
