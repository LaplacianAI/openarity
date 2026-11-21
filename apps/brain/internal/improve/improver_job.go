package improve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/improvements"
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
	ListPatterns(ctx context.Context, minRuns int32) ([]db.ListPatternsRow, error)
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
	id, err := dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		id, err := j.improver.Begin(sctx, plan)
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
		return id, err
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

// sweepPatterns queues a pass for each agent with a pattern over the bar,
// at most one an hour each.
func (j *ImproverJob) sweepPatterns(ctx context.Context, now time.Time) error {
	rows, err := j.sweeps.ListPatterns(ctx, patternBar)
	if err != nil {
		return fmt.Errorf("listing patterns: %w", err)
	}
	var failed []error
	for _, p := range patternPasses(rows) {
		id := "improver:" + p.AgentID.String() + ":" + now.UTC().Format(idHour)
		if err := j.enqueue(p, id); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

// sweepDaily queues a pass for each agent with new digests or a prune
// candidate, once a day.
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
		p := plannedPass{TeamID: a.TeamID, AgentID: a.ID, Trigger: triggerDaily, Prune: prune}
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

// pruneFor is an agent's prune candidates not looked at lately: what it
// held in each of its last runs and never used.
func (im *Improver) pruneFor(ctx context.Context, agentID uuid.UUID) ([]string, error) {
	runs, err := im.store.ListAgentRecentRuns(ctx, db.ListAgentRecentRunsParams{AgentID: &agentID, PageSize: pruneWindow})
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's runs: %w", agentID, err)
	}
	if len(runs) < pruneWindow {
		return nil, nil
	}
	use, err := im.store.ListRunsUse(ctx, runs)
	if err != nil {
		return nil, err
	}
	a, err := im.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	var st improvements.AgentState
	if err := im.store.InTx(ctx, func(q *db.Queries) error {
		st, err = improvements.AgentNow(ctx, q, a)
		return err
	}); err != nil {
		return nil, err
	}
	m := material{agent: a, state: st}
	if err := im.lookups(ctx, &m); err != nil {
		return nil, err
	}
	var skills []string
	for _, id := range st.Agent.Skills {
		skills = append(skills, m.skillName(id))
	}
	var tools []grantedTool
	for _, g := range st.Agent.MCPServers {
		s, ok := m.servers[m.serverName(g.ID)]
		if !ok {
			continue
		}
		allowed := g.Allow
		if allowed == nil {
			allowed = s.Tools
		}
		for _, t := range allowed {
			run := s.Name + "__" + t
			if s.Bare {
				run = t
			}
			tools = append(tools, grantedTool{runName: run, server: s.Name, tool: t})
		}
	}
	looked, err := im.store.ListRecentPruneLooks(ctx, db.ListRecentPruneLooksParams{AgentID: agentID, Since: im.now().Add(-pruneQuiet)})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(pruneCandidates(runs, use, skills, tools), func(c string) bool {
		return slices.Contains(looked, c)
	}), nil
}
