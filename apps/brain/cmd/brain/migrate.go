package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

func migrate(ctx context.Context, logger *slog.Logger, dbStore *store.Store, dsn string, d direction) error {
	switch d {
	case directionUp:
		return migrateUp(ctx, logger, dbStore, dsn)
	case directionDown:
		return migrateDown(ctx, logger, dbStore)
	default:
		return fmt.Errorf("unhandled direction %q", d)
	}
}

func migrateUp(ctx context.Context, logger *slog.Logger, dbStore *store.Store, dsn string) error {
	applied, err := dbStore.Migrate(ctx)
	if err != nil {
		return err
	}
	logger.Info("Applied migrations", "count", applied)

	queue, err := dbos.NewClient(ctx, dbos.ClientConfig{DatabaseURL: dsn, AppName: worker.AppName, Logger: logger})
	if err != nil {
		return fmt.Errorf("create the durable runtime's tables: %w", err)
	}
	_ = dbos.Shutdown(queue, runQueueShutdown)
	logger.Info("The durable runtime's tables are current")
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
