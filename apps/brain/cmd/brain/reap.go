package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/reaper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func newEffects(
	ctx context.Context, cfg *config.Config,
	logger *slog.Logger, dbStore *store.Store,
) ([]reaper.Effect, error) {
	secretStore := newSecretStore(cfg, logger)
	if err := checkSecretStore(ctx, secretStore); err != nil {
		return nil, err
	}

	secretWriter, ok := secretStore.(secrets.Writer)
	if !ok {
		return nil, fmt.Errorf("the secret store (%T) cannot delete, so a deleted "+
			"team's key would outlive it", secretStore)
	}

	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return nil, err
	}

	return []reaper.Effect{
		reaper.Secrets(dbStore, secretWriter),
		reaper.Objects(dbStore, attachments),
	}, nil
}

func reap(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	effects, err := newEffects(ctx, cfg, logger, dbStore)
	if err != nil {
		return err
	}
	return reaper.SweepAll(ctx, logger, effects...)
}
