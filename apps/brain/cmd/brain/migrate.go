package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func migrate(ctx context.Context, logger *slog.Logger, dbStore *store.Store, d direction) error {
	switch d {
	case directionUp:
		return migrateUp(ctx, logger, dbStore)
	case directionDown:
		return migrateDown(ctx, logger, dbStore)
	default:
		return fmt.Errorf("unhandled direction %q", d)
	}
}

func migrateUp(ctx context.Context, logger *slog.Logger, dbStore *store.Store) error {
	applied, err := dbStore.Migrate(ctx)
	if err != nil {
		return err
	}
	logger.Info("Applied migrations", "count", applied)
	return nil
}

func migrateDown(ctx context.Context, logger *slog.Logger, dbStore *store.Store) error {
	err := dbStore.Rollback(ctx)
	if err != nil {
		return err
	}
	logger.Info("Rolled back migrations")
	return nil
}
