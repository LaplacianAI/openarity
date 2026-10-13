package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teamgraph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

const (
	graphStartTimeout = 5 * time.Second
	embeddingTimeout  = 30 * time.Second
)

func newProjector(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, secretStore secrets.Store,
) (*graph.Projector, func(), error) {
	key, err := embeddingKey(ctx, cfg, secretStore)
	if err != nil {
		return nil, nil, err
	}

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

	embedder := graph.NewEmbedder(cfg.OmniRouteURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions,
		key, &http.Client{Timeout: embeddingTimeout})

	return graph.NewProjector(dbStore, g, embedder, logger), func() { _ = g.Close() }, nil
}

func embeddingKey(
	ctx context.Context, cfg *config.Config, secretStore secrets.Store,
) (func(context.Context) (string, error), error) {
	if cfg.EmbeddingKeyRef == "" {
		return nil, nil
	}

	path, name, _ := strings.Cut(cfg.EmbeddingKeyRef, "#")
	read := func(ctx context.Context) (string, error) {
		return secretStore.Get(ctx, path, name)
	}

	ctx, cancel := context.WithTimeout(ctx, secretStoreTimeout)
	defer cancel()

	value, err := read(ctx)
	if err == nil && value == "" {
		err = errors.New("it is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("EMBEDDING_KEY_REF names %s, which cannot be read: %w", cfg.EmbeddingKeyRef, err)
	}
	return read, nil
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
