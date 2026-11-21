package improve

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	outcomeQueue    = "openarity-outcomes"
	outcomeWorkflow = "openarity.improve.outcome"
	digestQueue     = "openarity-digests"
	digestWorkflow  = "openarity.improve.digest"
	// Checks and digests a worker runs at once. Each is one model call
	// nobody waits on, so a few at a time is plenty.
	checksPerWorker = 4
	digestsPerWkr   = 2

	sweepWorkflow = "openarity.improve.sweep"
	sweepSchedule = "improve-sweep"
	sweepCron     = "15 * * * * *"

	// A run settles when a person writes again after it, or after this.
	settleAfter = 10 * time.Minute
	sweepPage   = 200
)

// Job judges and digests a team's runs in the background.
type Job struct {
	checker *Checker
	dbos    dbos.Context
}

func NewJob(c *Checker) *Job { return &Job{checker: c} }

func (*Job) Name() string { return "improve" }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.checker == nil {
		return nil, errors.New("the improve job has no checker, so no run would be judged")
	}
	if _, err := dbos.RegisterQueue(d, outcomeQueue, dbos.WithWorkerConcurrency(checksPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the outcome queue: %w", err)
	}
	if _, err := dbos.RegisterQueue(d, digestQueue, dbos.WithWorkerConcurrency(digestsPerWkr)); err != nil {
		return nil, fmt.Errorf("registering the digest queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.outcome, dbos.WithWorkflowName(outcomeWorkflow))
	dbos.RegisterWorkflow(d, j.digest, dbos.WithWorkflowName(digestWorkflow))
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(sweepWorkflow))
	j.dbos = d
	// No backfill: a missed tick has no work of its own; the next finds it.
	return []dbos.ScheduleSpec{{
		ScheduleName: sweepSchedule, Schedule: sweepCron, WorkflowName: sweepWorkflow, AutomaticBackfill: false,
	}}, nil
}

func (j *Job) outcome(ctx dbos.Context, runID string) (string, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return runID, err
	}
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return runID, j.checker.Check(sctx, id)
	}, dbos.WithStepMaxRetries(3))
}

// digest's input is the run id and its round, the round only in the
// workflow id: a digest reopened by a later 👎 runs again.
func (j *Job) digest(ctx dbos.Context, runID string) (string, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return runID, err
	}
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return runID, j.checker.Digest(sctx, id)
	}, dbos.WithStepMaxRetries(3))
}

func (j *Job) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepOnce(sctx)
	})
}

// sweepOnce queues every settled run for its check and every pending digest.
// Each workflow id is the run's, so a run swept twice is judged once.
func (j *Job) sweepOnce(ctx context.Context) error {
	settled, err := j.checker.store.ListSettledRuns(ctx, db.ListSettledRunsParams{
		SettleSeconds: int32(settleAfter.Seconds()), PageSize: sweepPage,
	})
	if err != nil {
		return fmt.Errorf("listing settled runs: %w", err)
	}
	var failed []error
	for _, r := range settled {
		if err := enqueueOutcome(j.dbos, r.ID); err != nil {
			failed = append(failed, err)
		}
	}

	pending, err := j.checker.store.ListPendingDigests(ctx, sweepPage)
	if err != nil {
		return errors.Join(append(failed, fmt.Errorf("listing pending digests: %w", err))...)
	}
	for _, p := range pending {
		if err := enqueueDigest(j.dbos, p.RunID, p.DigestRound); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func enqueueOutcome(c dbos.Client, runID uuid.UUID) error {
	_, err := dbos.Enqueue[string](c, outcomeQueue, outcomeWorkflow, runID.String(),
		dbos.WithEnqueueWorkflowID("outcome:"+runID.String()))
	if err != nil {
		return fmt.Errorf("queuing run %s's check: %w", runID, err)
	}
	return nil
}

func enqueueDigest(c dbos.Client, runID uuid.UUID, round int32) error {
	_, err := dbos.Enqueue[string](c, digestQueue, digestWorkflow, runID.String(),
		dbos.WithEnqueueWorkflowID("digest:"+runID.String()+":"+strconv.Itoa(int(round))))
	if err != nil {
		return fmt.Errorf("queuing run %s's digest: %w", runID, err)
	}
	return nil
}
