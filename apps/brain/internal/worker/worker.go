package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	AppName = "openarity-brain"
	// appVersion is fixed, not DBOS's default hash of the binary: a worker
	// dequeues only workflows of its own version, so with the hash a run
	// stopped by one deploy is never picked up by the next. A run's workflow
	// is one step that rebuilds the run from its rows, so any build can carry
	// on any run.
	//
	// A workflow keeps the version it was enqueued with, and neither a resume
	// nor a second enqueue of its id restamps it. So a change here strands
	// every run queued or PENDING under the old value: change it only when a
	// workflow's registered name, input type or steps change in a way such a
	// run cannot replay, and restamp with the runbook in DECISIONS.md.
	// DBOS__APPVERSION, if set, overrides this.
	appVersion = "openarity-brain-v1"
)

const (
	shutdownGrace = 30 * time.Second
	// startupTimeout bounds the system database's connect and migrations.
	// A signal cannot cut them short, as DBOS's context is made without
	// ctx's cancellation.
	startupTimeout = 60 * time.Second
)

func executorID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

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

func (w *Worker) config() dbos.Config {
	return dbos.Config{
		AppName:                AppName,
		ApplicationVersion:     appVersion,
		DatabaseURL:            w.dsn,
		Logger:                 w.logger,
		ExecutorID:             executorID(),
		SystemDBStartupTimeout: startupTimeout,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if len(w.jobs) == 0 {
		return fmt.Errorf("the worker has no jobs, so it would run forever " +
			"doing nothing — a process that looks healthy and erases nothing")
	}

	// Not ctx itself: a signal cancelling ctx would reach every workflow as
	// a cancellation, which DBOS records for good. Shutdown, below, stops them
	// as a shutdown instead, leaving them PENDING for the next worker.
	d, err := dbos.NewContext(context.WithoutCancel(ctx), w.config())
	if err != nil {
		return fmt.Errorf("start the durable runtime: %w", err)
	}
	defer func() {
		if err := dbos.Shutdown(d, shutdownGrace); err != nil {
			w.logger.Warn("The durable runtime did not shut down cleanly", "error", err)
		}
	}()

	schedules, err := w.register(d)
	if err != nil {
		return err
	}

	if err := d.Launch(); err != nil {
		return fmt.Errorf("launch the durable runtime: %w", err)
	}
	// The API enqueues a run with no version, and DBOS hands such a workflow
	// only to a worker of the latest version: the highest version_timestamp,
	// set when a version is first registered and moved to now by this call.
	// One launch of any other build, a rollback say, would otherwise leave
	// every new run queued for good. The context's version, not appVersion,
	// as DBOS__APPVERSION overrides the config.
	version := dbos.GetApplicationVersion(d)
	if err := dbos.SetLatestApplicationVersion(d, version); err != nil {
		return fmt.Errorf("make %s the latest version: %w", version, err)
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
