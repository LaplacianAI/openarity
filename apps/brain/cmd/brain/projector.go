package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teamgraph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

const (
	graphStartTimeout = 5 * time.Second
	embeddingTimeout  = 30 * time.Second
)

func newProjector(
	ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store,
) (*graph.Projector, func(), error) {
	startCtx, cancel := context.WithTimeout(ctx, graphStartTimeout)
	defer cancel()

	g, err := graph.Dial(startCtx, cfg.FalkorDBURL, cfg.EmbeddingDimensions)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to FalkorDB: %w", err)
	}
	if err := g.Ping(startCtx); err != nil {
		_ = g.Close()
		return nil, nil, fmt.Errorf("FalkorDB unreachable at startup: %w", err)
	}

	embedder := graph.NewEmbedder(cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions,
		embeddingKey(cfg), &http.Client{Timeout: embeddingTimeout})

	return graph.NewProjector(dbStore, g, embedder, logger), func() { _ = g.Close() }, nil
}

func embeddingKey(cfg *config.Config) func(context.Context) (string, error) {
	if cfg.EmbeddingAPIKey == "" {
		return nil
	}
	return func(context.Context) (string, error) { return cfg.EmbeddingAPIKey, nil }
}

func newGraphViewer(ctx context.Context, cfg *config.Config, logger *slog.Logger) (teamgraph.Viewer, func()) {
	if cfg.EmbeddingModel == "" {
		return nil, func() {}
	}

	dialCtx, cancel := context.WithTimeout(ctx, graphStartTimeout)
	defer cancel()

	g, err := graph.Dial(dialCtx, cfg.FalkorDBURL, cfg.EmbeddingDimensions)
	if err != nil {
		logger.WarnContext(ctx, "The graph view is off: FalkorDB could not be reached", "error", err)
		return nil, func() {}
	}
	return g, func() { _ = g.Close() }
}
