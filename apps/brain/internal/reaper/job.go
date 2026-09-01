package reaper

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	sweepCron     = "0 */15 * * * *"
	sweepWorkflow = "openarity.reaper.sweep"
	sweepSchedule = "reaper-sweep"
)

func SweepAll(ctx context.Context, logger *slog.Logger, effects ...Effect) error {
	var overdue []string

	for _, effect := range effects {
		res, err := New(effect, logger).Sweep(ctx)
		if err != nil {
			return err
		}

		logger.Info("Swept",
			"effect", res.Effect,
			"applied", res.Applied,
			"superseded", res.Superseded,
			"failed", res.Failed,
			"outstanding", res.Outstanding,
		)

		if res.Overdue() {
			overdue = append(overdue,
				fmt.Sprintf("%s: %d outstanding, oldest %s",
					res.Effect, res.Outstanding, res.Oldest))
		}
	}

	if len(overdue) > 0 {
		return fmt.Errorf("%w: %s", ErrOverdue, strings.Join(overdue, "; "))
	}
	return nil
}

type sweepJob struct {
	logger  *slog.Logger
	effects []Effect
}

func SweepJob(logger *slog.Logger, effects ...Effect) sweepJob {
	return sweepJob{logger: logger, effects: effects}
}

func (sweepJob) Name() string { return "reaper.sweep" }

func (j sweepJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if len(j.effects) == 0 {
		return nil, fmt.Errorf("the sweep job has no effects, so it would run " +
			"every fifteen minutes and erase nothing")
	}

	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(sweepWorkflow))
	return j.schedules(), nil
}

func (sweepJob) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{
		ScheduleName:      sweepSchedule,
		Schedule:          sweepCron,
		WorkflowName:      sweepWorkflow,
		AutomaticBackfill: true,
	}}
}

func (j sweepJob) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		if err := SweepAll(sctx, j.logger, j.effects...); err != nil {
			return "", err
		}
		return in.ScheduledTime.UTC().Format(time.RFC3339), nil
	})
}
