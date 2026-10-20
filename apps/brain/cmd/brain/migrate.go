package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

// crc32("openarity/dbos"), beside migrationLockID's crc32("openarity").
// DBOS migrates its tables with no lock and does not retry the duplicate key
// two migrators racing produce; every replica of a rollout runs migrate up.
const dbosMigrationLockID int64 = 2405736231

const dbosMigrationRetry = 250 * time.Millisecond

func migrateUp(ctx context.Context, logger *slog.Logger, dbStore *store.Store, dsn string) error {
	applied, err := dbStore.Migrate(ctx)
	if err != nil {
		return err
	}
	logger.Info("Applied migrations", "count", applied)

	for {
		held, err := dbStore.WithAdvisoryLock(ctx, dbosMigrationLockID, func(ctx context.Context) error {
			return migrateDBOS(ctx, logger, dsn)
		})
		if err != nil || held {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dbosMigrationRetry):
		}
	}
}

func migrateDBOS(ctx context.Context, logger *slog.Logger, dsn string) error {
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
