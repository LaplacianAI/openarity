package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func rebuildGraph(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, team uuid.UUID,
) error {
	if cfg.EmbeddingModel == "" {
		return errors.New("EMBEDDING_MODEL is not set, so there is no model to build the graph with")
	}

	projector, closeGraph, err := newProjector(ctx, cfg, logger, dbStore, newSecretStore(cfg, logger))
	if err != nil {
		return err
	}
	defer closeGraph()

	teams := []uuid.UUID{team}
	if team == uuid.Nil {
		if teams, err = dbStore.ListTeamIDs(ctx); err != nil {
			return fmt.Errorf("listing the teams to rebuild: %w", err)
		}
	}

	failed := 0
	for _, id := range teams {
		projected, err := projector.Rebuild(ctx, id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.ErrorContext(ctx, "Graph not rebuilt", "team_id", id, "projected", projected, "error", err)
			failed++
			continue
		}
		logger.InfoContext(ctx, "Graph rebuilt", "team_id", id, "projected", projected)
	}

	if failed > 0 {
		return fmt.Errorf("the graph was not rebuilt for %d of %d teams", failed, len(teams))
	}
	return nil
}
