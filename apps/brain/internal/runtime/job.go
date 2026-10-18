package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
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
	ListUnstartedRuns(ctx context.Context, olderThanSeconds float64) ([]uuid.UUID, error)
	TakeOverStaleRuns(ctx context.Context, staleSeconds float64) ([]uuid.UUID, error)
}

type Job struct {
	executor *Executor
	store    SweepStore
	logger   *slog.Logger
	dbos     dbos.Context
	restart  func(uuid.UUID) error
}

func RunJob(e *Executor, s SweepStore, logger *slog.Logger) *Job {
	j := &Job{executor: e, store: s, logger: logger}
	j.restart = j.restartRun
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
	for _, id := range queued {
		if err := j.restart(id); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (j *Job) restartRun(id uuid.UUID) error {
	if err := enqueue(j.dbos, id); err != nil {
		return err
	}
	if _, err := dbos.ResumeWorkflow[string](j.dbos, id.String(), dbos.WithResumeQueue(RunQueue)); err != nil {
		return fmt.Errorf("resuming run %s: %w", id, err)
	}
	return nil
}

func enqueue(c dbos.Client, id uuid.UUID) error {
	_, err := dbos.Enqueue[string](c, RunQueue, RunWorkflow, id.String(), dbos.WithEnqueueWorkflowID(id.String()))
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
	return enqueue(q.client, runID)
}
