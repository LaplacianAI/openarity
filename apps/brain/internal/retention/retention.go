// Package retention deletes the durable runtime's record of background work
// that finished long ago, which DBOS itself never deletes.
//
// It deletes only what is periodic by construction: every workflow DBOS runs
// on a schedule (a sweep's ticks, whose output nobody reads), and the
// workflows another job names as its own batches. A run's workflow is never
// one of them; the runs table is its record, and pruning those is a decision
// of its own.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// Names DBOS stores: renaming one orphans what it recorded.
const (
	workflow = "openarity.retention"
	schedule = "dbos-retention"
	daily    = "0 40 3 * * *"
)

const (
	// Keep is how long finished work stays recorded.
	Keep        = 7 * 24 * time.Hour
	deleteBatch = 500
	maxDeletes  = 40
)

// A Job deletes, once a day, the record of finished scheduled work, and of
// the batches it was told of, older than Keep.
type Job struct {
	batches []string
	logger  *slog.Logger
	dbos    dbos.Context
	now     func() time.Time
}

// NewJob deletes old scheduled work, and old runs of the workflows batches
// names: work another job hands out from its schedule.
func NewJob(logger *slog.Logger, batches ...string) *Job {
	return &Job{batches: batches, logger: logger, now: time.Now}
}

func (*Job) Name() string { return "retention" }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if d == nil {
		return nil, errors.New("retention needs the durable runtime it deletes from")
	}
	dbos.RegisterWorkflow(d, j.retain, dbos.WithWorkflowName(workflow))
	j.dbos = d
	return j.schedules(), nil
}

func (*Job) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{ScheduleName: schedule, Schedule: daily, WorkflowName: workflow, AutomaticBackfill: false}}
}

func (j *Job) retain(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		n, err := j.once(sctx)
		return fmt.Sprintf("%s: %d", in.ScheduledTime.UTC().Format(time.RFC3339), n), err
	})
}

// once deletes what finished before Keep, a batch at a time, and says how
// many it deleted. DBOS is called with the root context, so its calls are not
// recorded as steps of this workflow.
func (j *Job) once(ctx context.Context) (int, error) {
	names, err := j.names()
	if err != nil {
		return 0, err
	}
	before := j.now().Add(-Keep)
	deleted := 0
	for range maxDeletes {
		old, err := dbos.ListWorkflows(j.dbos,
			dbos.WithFilterName(names...),
			dbos.WithFilterStatus(dbos.WorkflowStatusSuccess, dbos.WorkflowStatusError,
				dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded),
			dbos.WithFilterCompletedBefore(before),
			dbos.WithFilterLimit(deleteBatch),
			dbos.WithFilterLoadInput(false), dbos.WithFilterLoadOutput(false))
		if err != nil {
			return deleted, fmt.Errorf("listing finished background work: %w", err)
		}
		if len(old) == 0 {
			break
		}
		ids := make([]string, len(old))
		for i, w := range old {
			ids[i] = w.ID
		}
		if err := dbos.DeleteWorkflows(j.dbos, ids); err != nil {
			return deleted, fmt.Errorf("deleting finished background work: %w", err)
		}
		deleted += len(ids)
		if len(old) < deleteBatch {
			break
		}
	}
	if deleted > 0 {
		j.logger.InfoContext(ctx, "Deleted the durable runtime's record of finished background work", "workflows", deleted)
	}
	return deleted, nil
}

// names is every scheduled workflow, as DBOS holds them, and the batches.
func (j *Job) names() ([]string, error) {
	scheduled, err := dbos.ListSchedules(j.dbos)
	if err != nil {
		return nil, fmt.Errorf("listing the schedules DBOS runs: %w", err)
	}
	names := append([]string{}, j.batches...)
	for _, s := range scheduled {
		names = append(names, s.WorkflowName)
	}
	return names, nil
}
