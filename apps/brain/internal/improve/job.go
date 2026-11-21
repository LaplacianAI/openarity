package improve

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	checksPerWorker  = 4
	digestsPerWorker = 2

	sweepWorkflow = "openarity.improve.sweep"
	sweepSchedule = "improve-sweep"
	sweepCron     = "15 * * * * *"

	// A run settles when a person writes again after it, or after this.
	settleAfter = 10 * time.Minute
	sweepPage   = 200

	// A model that cannot be reached is tried this many times more, waiting
	// 5s, 10s, 20s, 40s and 80s between: a busy or rate-limited endpoint
	// gets a few minutes in each hour's try. A run is given up on only
	// once giveUpAfter has passed; until then the next hour tries again.
	modelRetries = 5
	retryFirst   = 5 * time.Second
	retryLongest = 2 * time.Minute

	// The give-up and skip steps only write to the store, so they are
	// tried a few times on its account alone.
	giveUpRetries = 3

	// A workflow id holds the hour it was queued in. Within the hour a
	// run swept again is the same workflow, judged once; in a later hour
	// one that ended without writing (its model unreachable within the
	// run's first day, the store down while it gave up, or the team off
	// while it ran) is queued again, rather than listed every minute and
	// never run.
	idHour = "2006010215"
)

// Queues is what the sweep reads: the work waiting.
type Queues interface {
	ListSettledRuns(ctx context.Context, arg db.ListSettledRunsParams) ([]db.ListSettledRunsRow, error)
	ListPendingDigests(ctx context.Context, pageSize int32) ([]db.ListPendingDigestsRow, error)
}

// Job judges and digests a team's runs in the background.
type Job struct {
	checker *Checker
	queues  Queues
	dbos    dbos.Context
}

func NewJob(c *Checker, q Queues) *Job { return &Job{checker: c, queues: q} }

func (*Job) Name() string { return "improve" }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.checker == nil || j.queues == nil {
		return nil, errors.New("the improve job has no checker or queues, so no run would be judged")
	}
	if _, err := dbos.RegisterQueue(d, outcomeQueue, dbos.WithWorkerConcurrency(checksPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the outcome queue: %w", err)
	}
	if _, err := dbos.RegisterQueue(d, digestQueue, dbos.WithWorkerConcurrency(digestsPerWorker)); err != nil {
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

// retrying is how a step that asks the model is tried again: with backoff,
// and never once the worker is shutting down, when the workflow is left
// for recovery to resume.
func retrying(name string) []dbos.StepOption {
	return []dbos.StepOption{
		dbos.WithStepName(name),
		dbos.WithStepMaxRetries(modelRetries),
		dbos.WithStepBaseInterval(retryFirst),
		dbos.WithStepMaxInterval(retryLongest),
		dbos.WithStepRetryPredicate(func(err error) bool { return !errors.Is(err, context.Canceled) }),
	}
}

// stopped is whether a step's end leaves nothing to give up on: it worked,
// or it was cancelled (the worker shutting down, or the call), which is
// never the model's failure and is left for recovery or the next sweep.
func stopped(ctx context.Context, err error) bool {
	return err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func (j *Job) outcome(ctx dbos.Context, runID string) (string, error) {
	id, err := uuid.Parse(runID)
	if err != nil {
		return runID, err
	}
	_, failed := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return runID, j.checker.Check(sctx, id)
	}, retrying("check")...)
	if stopped(ctx, failed) {
		return runID, failed
	}
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return runID, j.checker.GiveUp(sctx, id, failed)
	}, dbos.WithStepName("give up"), dbos.WithStepMaxRetries(giveUpRetries))
}

// digest's input is the run id and the round it was queued for, so a
// digest reopened by a later 👎 runs again, and one of an old round
// writes nothing.
func (j *Job) digest(ctx dbos.Context, input string) (string, error) {
	id, round, err := parseDigestInput(input)
	if err != nil {
		return input, err
	}
	_, failed := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return input, j.checker.Digest(sctx, id, round)
	}, retrying("digest")...)
	if stopped(ctx, failed) {
		return input, failed
	}
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return input, j.checker.SkipDigest(sctx, id, round, failed)
	}, dbos.WithStepName("skip"), dbos.WithStepMaxRetries(giveUpRetries))
}

func digestInput(runID uuid.UUID, round int32) string {
	return runID.String() + ":" + strconv.Itoa(int(round))
}

func parseDigestInput(s string) (uuid.UUID, int32, error) {
	run, round, ok := strings.Cut(s, ":")
	if !ok {
		return uuid.UUID{}, 0, fmt.Errorf("digest input %q is not run:round", s)
	}
	id, err := uuid.Parse(run)
	if err != nil {
		return uuid.UUID{}, 0, err
	}
	n, err := strconv.ParseInt(round, 10, 32)
	if err != nil {
		return uuid.UUID{}, 0, fmt.Errorf("digest input %q: %w", s, err)
	}
	return id, int32(n), nil
}

func (j *Job) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepOnce(sctx, in.ScheduledTime)
	})
}

// sweepOnce queues every settled run for its check and every pending digest.
func (j *Job) sweepOnce(ctx context.Context, now time.Time) error {
	hour := now.UTC().Format(idHour)
	settled, err := j.queues.ListSettledRuns(ctx, db.ListSettledRunsParams{
		SettleSeconds: int32(settleAfter.Seconds()), PageSize: sweepPage,
	})
	if err != nil {
		return fmt.Errorf("listing settled runs: %w", err)
	}
	var failed []error
	for _, r := range settled {
		if err := enqueueOutcome(j.dbos, r.ID, hour); err != nil {
			failed = append(failed, err)
		}
	}

	pending, err := j.queues.ListPendingDigests(ctx, sweepPage)
	if err != nil {
		return errors.Join(append(failed, fmt.Errorf("listing pending digests: %w", err))...)
	}
	for _, p := range pending {
		if err := enqueueDigest(j.dbos, p.RunID, p.DigestRound, hour); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func enqueueOutcome(c dbos.Client, runID uuid.UUID, hour string) error {
	_, err := dbos.Enqueue[string](c, outcomeQueue, outcomeWorkflow, runID.String(),
		dbos.WithEnqueueWorkflowID("outcome:"+runID.String()+":"+hour))
	if err != nil {
		return fmt.Errorf("queuing run %s's check: %w", runID, err)
	}
	return nil
}

func enqueueDigest(c dbos.Client, runID uuid.UUID, round int32, hour string) error {
	_, err := dbos.Enqueue[string](c, digestQueue, digestWorkflow, digestInput(runID, round),
		dbos.WithEnqueueWorkflowID("digest:"+digestInput(runID, round)+":"+hour))
	if err != nil {
		return fmt.Errorf("queuing run %s's digest: %w", runID, err)
	}
	return nil
}
