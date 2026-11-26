package triggers

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

// Names DBOS stores: renaming one orphans what it recorded.
const (
	tickWorkflow = "openarity.triggers.tick"
	tickSchedule = "triggers-tick"
	tickCron     = "0 * * * * *"

	// FireWorkflow is a batch of fires a tick hands out.
	FireWorkflow   = "openarity.triggers.fire"
	FireQueue      = "openarity-triggers"
	firesPerWorker = 2

	hourlyWorkflow = "openarity.triggers.sweep"
	hourlySchedule = "triggers-sweep"
	hourlyCron     = "0 7 * * * *"
)

const (
	// fireBatch and fireBudget bound one fire workflow; maxFireWorkflows
	// bounds how many one tick starts.
	fireBatch        = 100
	fireBudget       = 45 * time.Second
	maxFireWorkflows = 8

	pruneBatch = 1000
	maxPrunes  = 20
)

// JobStore is what the jobs read and write.
type JobStore interface {
	FireStore
	ExpireProposedSchedules(ctx context.Context, before time.Time) (int64, error)
	PruneScheduleFires(ctx context.Context, arg db.PruneScheduleFiresParams) (int64, error)
}

// A Job fires schedules on a minute tick, and lapses proposals and prunes
// history hourly. The record DBOS keeps of its batches is deleted by the
// retention job, told of them as FireWorkflow.
type Job struct {
	store   JobStore
	logger  *slog.Logger
	enqueue func(dbos.Client, uuid.UUID) error
	firer   *Firer
	dbos    dbos.Context
	now     func() time.Time
}

// NewJob takes how a run is handed to the workers: the runtime's.
func NewJob(s JobStore, enqueueRun func(dbos.Client, uuid.UUID) error, logger *slog.Logger) *Job {
	return &Job{store: s, logger: logger, enqueue: enqueueRun, now: time.Now}
}

func (*Job) Name() string { return "triggers" }

// enqueuerFunc hands a run to the workers through the runtime's function.
type enqueuerFunc func(context.Context, uuid.UUID) error

func (f enqueuerFunc) Enqueue(ctx context.Context, id uuid.UUID) error { return f(ctx, id) }

func (j *Job) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	if j.store == nil || j.enqueue == nil {
		return nil, errors.New("the triggers job has no store or no way to start a run, so every fire would fail")
	}
	if _, err := dbos.RegisterQueue(d, FireQueue, dbos.WithWorkerConcurrency(firesPerWorker)); err != nil {
		return nil, fmt.Errorf("registering the fire queue: %w", err)
	}
	dbos.RegisterWorkflow(d, j.tick, dbos.WithWorkflowName(tickWorkflow))
	dbos.RegisterWorkflow(d, j.fire, dbos.WithWorkflowName(FireWorkflow))
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(hourlyWorkflow))
	j.dbos = d
	j.firer = NewFirer(j.store, enqueuerFunc(func(_ context.Context, id uuid.UUID) error { return j.enqueue(d, id) }), j.logger)
	j.firer.now = j.now
	return j.schedules(), nil
}

func (*Job) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{
		{ScheduleName: tickSchedule, Schedule: tickCron, WorkflowName: tickWorkflow, AutomaticBackfill: false},
		{ScheduleName: hourlySchedule, Schedule: hourlyCron, WorkflowName: hourlyWorkflow, AutomaticBackfill: false},
	}
}

// tick counts what is due and hands it out in batches, so every worker fires
// and SKIP LOCKED keeps two off one schedule. Its cost follows what is due,
// not how many schedules there are. A batch's id is the tick's minute and its
// place, so a tick run again starts nothing twice.
func (j *Job) tick(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		minute := in.ScheduledTime.UTC().Format("200601021504")
		n, err := j.firer.Due(sctx)
		if err != nil {
			return minute, err
		}
		for i := range batches(n) {
			if _, err := dbos.Enqueue[string](j.dbos, FireQueue, FireWorkflow, minute,
				dbos.WithEnqueueWorkflowID(fmt.Sprintf("fire:%s:%d", minute, i))); err != nil {
				return minute, fmt.Errorf("handing out fire batch %d: %w", i, err)
			}
		}
		return minute, nil
	})
}

// batches is how many fire workflows n due schedules take.
func batches(n int) int {
	return min((n+fireBatch-1)/fireBatch, maxFireWorkflows)
}

func (j *Job) fire(ctx dbos.Context, minute string) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		fired, err := j.firer.FireDue(sctx, fireBatch, fireBudget)
		return fmt.Sprintf("%s: %d", minute, fired), err
	})
}

func (j *Job) sweep(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (string, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (string, error) {
		return in.ScheduledTime.UTC().Format(time.RFC3339), j.sweepOnce(sctx)
	})
}

// sweepOnce lapses proposals nobody confirmed and keeps each schedule's
// newest fires.
func (j *Job) sweepOnce(ctx context.Context) error {
	var failed []error
	if n, err := j.store.ExpireProposedSchedules(ctx, j.now().Add(-proposalLife)); err != nil {
		failed = append(failed, fmt.Errorf("lapsing unconfirmed proposals: %w", err))
	} else if n > 0 {
		j.logger.InfoContext(ctx, "Lapsed schedules nobody confirmed", "schedules", n)
	}
	for range maxPrunes {
		n, err := j.store.PruneScheduleFires(ctx, db.PruneScheduleFiresParams{Keep: keepFires, Batch: pruneBatch})
		if err != nil {
			failed = append(failed, fmt.Errorf("pruning old fires: %w", err))
			break
		}
		if n < pruneBatch {
			break
		}
	}
	return errors.Join(failed...)
}
