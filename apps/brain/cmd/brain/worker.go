package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/reaper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

func runWorker(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	secretStore := newSecretStore(cfg, logger)

	effects, err := newEffects(ctx, cfg, logger, dbStore, secretStore)
	if err != nil {
		return err
	}

	jobs, closeJobs, err := newJobs(ctx, cfg, logger, dbStore, secretStore, effects)
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
	dbStore *store.Store, secretStore secrets.Store, effects []reaper.Effect,
) ([]worker.Job, func(), error) {
	sweep := reaper.SweepJob(logger, effects...)

	if cfg.EmbeddingModel == "" {
		logger.WarnContext(ctx, "EMBEDDING_MODEL is not set, so the graph is not being projected or searched")
		runs, err := newRunJob(cfg, logger, dbStore, secretStore, nil)
		if err != nil {
			return nil, nil, err
		}
		return []worker.Job{sweep, runs}, func() {}, nil
	}

	g, embedder, closeGraph, err := dialGraph(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	runs, err := newRunJob(cfg, logger, dbStore, secretStore,
		&runtime.GraphDeps{Search: g, Embed: embedder, Store: dbStore})
	if err != nil {
		closeGraph()
		return nil, nil, err
	}
	projector := graph.NewProjector(dbStore, g, embedder, logger)
	return []worker.Job{sweep, runs, graph.DrainJob(projector, logger)}, closeGraph, nil
}
