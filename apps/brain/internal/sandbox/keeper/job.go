package keeper

import (
	"context"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	templatesCron     = "*/30 * * * * *"
	templatesWorkflow = "openarity.sandbox.e2b-templates"
	templatesSchedule = "e2b-templates"
)

type templatesJob struct{ t *Templates }

// TemplatesJob reads back the E2B template builds still running, every
// thirty seconds.
func TemplatesJob(t *Templates) templatesJob {
	if t == nil {
		panic("keeper: a templates job with no builder")
	}
	return templatesJob{t: t}
}

func (templatesJob) Name() string { return "sandbox.e2b-templates" }

func (j templatesJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(templatesWorkflow))
	return j.schedules(), nil
}

// No backfill: a missed tick has no work of its own; the next reads every
// build still running.
func (templatesJob) schedules() []dbos.ScheduleSpec {
	return []dbos.ScheduleSpec{{
		ScheduleName:      templatesSchedule,
		Schedule:          templatesCron,
		WorkflowName:      templatesWorkflow,
		AutomaticBackfill: false,
	}}
}

func (j templatesJob) sweep(ctx dbos.Context, _ dbos.ScheduledWorkflowInput) (struct{}, error) {
	return dbos.RunAsStep(ctx, func(sctx context.Context) (struct{}, error) {
		return struct{}{}, j.t.Sweep(sctx)
	})
}
