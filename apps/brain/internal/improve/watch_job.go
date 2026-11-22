package improve

import (
	"context"
	"errors"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	watchSweepFlow = "openarity.improve.watch-sweep"
	watchSchedule  = "improve-watch-sweep"
	// Every ten minutes, five after the outcome sweep, so the runs it just
	// judged are counted.
	watchCron  = "0 5-59/10 * * * *"
	watchEvery = 10 * time.Minute
)

// WatchJob counts how each applied change went, every ten minutes.
type WatchJob struct {
	watcher *Watcher
}

func NewWatchJob(w *Watcher) *WatchJob { return &WatchJob{watcher: w} }

func (*WatchJob) Name() string { return "watch" }

func (j *WatchJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.watcher == nil {
		return nil, errors.New("the watch job has no watcher, so no change would be watched")
	}
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(watchSweepFlow))
	// No backfill: a missed sweep finds the same watches at the next, and
	// counts only the time it saw improvement on.
	return []dbos.ScheduleSpec{
		{ScheduleName: watchSchedule, Schedule: watchCron, WorkflowName: watchSweepFlow, AutomaticBackfill: false},
	}, nil
}

func (j *WatchJob) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.watcher.Sweep(sctx, watchEvery)
	})
}
