package graph

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	drainCron     = "0 * * * * *"
	drainWorkflow = "openarity.graph.drain"
	drainSchedule = "graph-drain"
	drainBatch    = 100

	behindAfter = 15 * time.Minute
	alarmEvery  = 15 * time.Minute
)

type drainJob struct {
	projector *Projector
	logger    *slog.Logger
}

func DrainJob(p *Projector, logger *slog.Logger) drainJob {
	return drainJob{projector: p, logger: logger}
}

func (drainJob) Name() string { return "graph.drain" }

func (j drainJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.projector == nil {
		return nil, errors.New("the drain job has no projector, so it would run " +
			"every minute and project nothing")
	}

	dbos.RegisterWorkflow(d, j.drain, dbos.WithWorkflowName(drainWorkflow))
	return j.schedules(), nil
}

func (drainJob) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{
		ScheduleName: drainSchedule,
		Schedule:     drainCron,
		WorkflowName: drainWorkflow,
	}}
}

func (j drainJob) drain(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		if err := j.run(sctx, in.ScheduledTime); err != nil {
			return "", err
		}
		return in.ScheduledTime.UTC().Format(time.RFC3339), nil
	})
}

func (j drainJob) run(ctx context.Context, tick time.Time) error {
	res, err := j.projector.Drain(ctx, drainBatch)
	if err != nil {
		return err
	}

	if res.Projected > 0 || res.Failed > 0 {
		j.logger.InfoContext(ctx, "Graph drained", "projected", res.Projected, "failed", res.Failed)
	}
	if tick.Truncate(alarmEvery).Equal(tick) {
		j.alarm(ctx)
	}
	return nil
}

func (j drainJob) alarm(ctx context.Context) {
	outstanding, oldest, err := j.projector.Backlog(ctx)
	if err != nil {
		j.logger.ErrorContext(ctx, "Reading the graph backlog", "error", err)
		return
	}
	if outstanding == 0 || time.Since(oldest) < behindAfter {
		return
	}

	j.logger.ErrorContext(ctx, "The graph is behind",
		"outstanding", outstanding, "oldest", oldest,
		"age", time.Since(oldest).Round(time.Minute))
}
