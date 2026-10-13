package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/reaper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

func runWorker(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	secretStore := newSecretStore(cfg, logger)

	effects, err := newEffects(ctx, cfg, logger, dbStore, secretStore)
	if err != nil {
		return err
	}

	jobs, closeJobs, err := newJobs(ctx, cfg, logger, dbStore, effects)
	if err != nil {
		return err
	}
	defer closeJobs()

	if err := reaper.SweepAll(ctx, logger, effects...); err != nil {
		if !errors.Is(err, reaper.ErrOverdue) {
			return err
		}
		logger.ErrorContext(ctx, "Starting with an erasure already overdue", "error", err)
	}

	return worker.New(cfg.PostgresDSN, logger, jobs...).Run(ctx)
}

func newJobs(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, effects []reaper.Effect,
) ([]worker.Job, func(), error) {
	jobs := []worker.Job{reaper.SweepJob(logger, effects...)}

	if cfg.EmbeddingModel == "" {
		logger.WarnContext(ctx, "EMBEDDING_MODEL is not set, so the graph is not being projected")
		return jobs, func() {}, nil
	}

	projector, closeGraph, err := newProjector(ctx, cfg, logger, dbStore)
	if err != nil {
		return nil, nil, err
	}
	return append(jobs, graph.DrainJob(projector, logger)), closeGraph, nil
}
