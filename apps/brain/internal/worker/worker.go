package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const shutdownGrace = 30 * time.Second

type Job interface {
	Name() string
	Register(d dbos.Context) ([]dbos.ScheduleSpec, error)
}

type Worker struct {
	dsn    string
	logger *slog.Logger
	jobs   []Job
}

func New(dsn string, logger *slog.Logger, jobs ...Job) *Worker {
	return &Worker{dsn: dsn, logger: logger, jobs: jobs}
}

func (w *Worker) Run(ctx context.Context) error {
	if len(w.jobs) == 0 {
		return fmt.Errorf("the worker has no jobs, so it would run forever " +
			"doing nothing — a process that looks healthy and erases nothing")
	}

	d, err := dbos.NewContext(ctx, dbos.Config{
		AppName:     "openarity-brain",
		DatabaseURL: w.dsn,
		Logger:      w.logger,
	})
	if err != nil {
		return fmt.Errorf("start the durable runtime: %w", err)
	}
	defer func() { _ = dbos.Shutdown(d, shutdownGrace) }()

	schedules, err := w.register(d)
	if err != nil {
		return err
	}

	if err := d.Launch(); err != nil {
		return fmt.Errorf("launch the durable runtime: %w", err)
	}

	if len(schedules) > 0 {
		if err := dbos.ApplySchedules(d, schedules); err != nil {
			return fmt.Errorf("install %d schedule(s): %w", len(schedules), err)
		}
	}

	w.logger.Info("Worker running", "jobs", len(w.jobs), "schedules", len(schedules))

	<-ctx.Done()
	w.logger.Info("Worker stopping")
	return nil
}

func (w *Worker) register(d dbos.Context) ([]dbos.ScheduleSpec, error) {
	var all []dbos.ScheduleSpec
	owner := make(map[string]string, len(w.jobs))

	for _, job := range w.jobs {
		specs, err := job.Register(d)
		if err != nil {
			return nil, fmt.Errorf("register the %q job: %w", job.Name(), err)
		}

		for _, spec := range specs {
			if first, dup := owner[spec.ScheduleName]; dup {
				return nil, fmt.Errorf(
					"jobs %q and %q both claim the schedule %q",
					first, job.Name(), spec.ScheduleName)
			}
			owner[spec.ScheduleName] = job.Name()
			all = append(all, spec)
		}

		w.logger.Info("Job registered", "job", job.Name(), "schedules", len(specs))
	}

	return all, nil
}
