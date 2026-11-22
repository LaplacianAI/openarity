package improve

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	improverQueue    = "openarity-improver"
	passWorkflow     = "openarity.improve.pass"
	patternSweepFlow = "openarity.improve.pattern-sweep"
	dailySweepFlow   = "openarity.improve.daily-sweep"
	patternSchedule  = "improve-pattern-sweep"
	dailySchedule    = "improve-daily-sweep"
	patternCron      = "0 */10 * * * *"
	dailyCron        = "0 30 3 * * *"
	// One pass at a time on a worker: each is a long model run nobody
	// waits on.
	passesPerWorker = 1
	// A pass that cannot reach its model is tried this many times more.
	passRetries    = 2
	passRetryFirst = 30 * time.Second
	passRetryMost  = 2 * time.Minute
	idDay          = "20060102"
)

// Sweeps is what the improver's sweeps read.
type Sweeps interface {
	ListPatterns(ctx context.Context, arg db.ListPatternsParams) ([]db.ListPatternsRow, error)
	ListImprovingAgents(ctx context.Context) ([]db.ListImprovingAgentsRow, error)
}

// ImproverJob finds an agent's patterns and runs a pass on them.
type ImproverJob struct {
	improver *Improver
	sweeps   Sweeps
	dbos     dbos.Context
}

func NewImproverJob(im *Improver, s Sweeps) *ImproverJob {
	return &ImproverJob{improver: im, sweeps: s}
}

func (*ImproverJob) Name() string { return "improver" }

func (j *ImproverJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.improver == nil || j.sweeps == nil {
		return nil, errors.New("the improver job has no improver or sweeps, so no pass would run")
	}
	if _, err := dbos.RegisterQueue(d, improverQueue, dbos.WithWorkerConcurrency(passesPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the improver queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.pass, dbos.WithWorkflowName(passWorkflow))
	dbos.RegisterWorkflow(d, j.patternSweep, dbos.WithWorkflowName(patternSweepFlow))
	dbos.RegisterWorkflow(d, j.dailySweep, dbos.WithWorkflowName(dailySweepFlow))
	j.dbos = d
	// No backfill: a missed sweep finds the same work at the next.
	return []dbos.ScheduleSpec{
		{ScheduleName: patternSchedule, Schedule: patternCron, WorkflowName: patternSweepFlow, AutomaticBackfill: false},
		{ScheduleName: dailySchedule, Schedule: dailyCron, WorkflowName: dailySweepFlow, AutomaticBackfill: false},
	}, nil
}

// pass begins, runs and finishes one pass. Begun is its own step, so a
// retried run reads the runs it was given rather than choosing again.
func (j *ImproverJob) pass(ctx dbos.Context, input string) (string, error) {
	plan, err := decodePlan(input)
	if err != nil {
		return "", err
	}
	workflowID, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the pass's workflow: %w", err)
	}
	id, err := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		id, err := j.improver.Begin(sctx, plan, workflowID)
		if errors.Is(err, errNoPass) {
			return "", nil
		}
		return id.String(), err
	}, dbos.WithStepName("begin"), dbos.WithStepMaxRetries(giveUpRetries))
	if err != nil || id == "" {
		return id, err
	}
	passID, err := uuid.Parse(id)
	if err != nil {
		return id, fmt.Errorf("reading the begun pass's id %q: %w", id, err)
	}
	_, failed := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return id, j.improver.Run(sctx, passID)
	},
		dbos.WithStepName("run"), dbos.WithStepMaxRetries(passRetries),
		dbos.WithStepBaseInterval(passRetryFirst), dbos.WithStepMaxInterval(passRetryMost),
		dbos.WithStepRetryPredicate(func(err error) bool { return !errors.Is(err, context.Canceled) }))
	if stopped(ctx, failed) {
		return id, failed
	}
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return id, j.improver.Fail(sctx, passID, failed)
	}, dbos.WithStepName("fail"), dbos.WithStepMaxRetries(giveUpRetries))
}

func (j *ImproverJob) patternSweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepPatterns(sctx, in.ScheduledTime)
	})
}

func (j *ImproverJob) dailySweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepDaily(sctx, in.ScheduledTime)
	})
}

// sweepPatterns fails the passes that were lost, then queues a pass for
// each agent with a pattern over the bar, at most one an hour each.
func (j *ImproverJob) sweepPatterns(ctx context.Context, now time.Time) error {
	lost := j.improver.FailLost(ctx)
	rows, err := j.sweeps.ListPatterns(ctx, db.ListPatternsParams{MinRuns: patternBar, BackOffHours: cutShortBackOffHours})
	if err != nil {
		return errors.Join(lost, fmt.Errorf("listing patterns: %w", err))
	}
	failed := []error{lost}
	for _, p := range patternPasses(rows) {
		id := "improver:" + p.AgentID.String() + ":" + now.UTC().Format(idHour)
		if err := j.enqueue(p, id); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

// sweepDaily queues a pass, once a day, for each agent with the team's
// daily runs judged since its last pass and something to read: a new digest
// or a prune candidate. One with fewer runs waits for more.
func (j *ImproverJob) sweepDaily(ctx context.Context, now time.Time) error {
	agents, err := j.sweeps.ListImprovingAgents(ctx)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	var failed []error
	for _, a := range agents {
		prune, err := j.improver.pruneFor(ctx, a.ID)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if !a.HasNew && len(prune) == 0 {
			continue
		}
		// The runs judged when the pass was planned; more may come before it begins.
		seen := a.RunsSince
		p := plannedPass{TeamID: a.TeamID, AgentID: a.ID, Trigger: triggerDaily, Prune: prune, RunsSeen: &seen}
		if err := j.enqueue(p, "improver:"+a.ID.String()+":daily:"+now.UTC().Format(idDay)); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (j *ImproverJob) enqueue(p plannedPass, id string) error {
	_, err := dbos.Enqueue[string](j.dbos, improverQueue, passWorkflow, p.encode(), dbos.WithEnqueueWorkflowID(id))
	if err != nil {
		return fmt.Errorf("queuing agent %s's pass: %w", p.AgentID, err)
	}
	return nil
}
