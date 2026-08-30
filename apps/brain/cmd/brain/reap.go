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

func reap(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	secretStore := newSecretStore(cfg, logger)
	if err := checkSecretStore(ctx, secretStore); err != nil {
		return err
	}

	secretWriter, ok := secretStore.(secrets.Writer)
	if !ok {
		return fmt.Errorf("the secret store (%T) cannot delete, so a deleted "+
			"team's key would outlive it", secretStore)
	}

	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return err
	}

	effects := []reaper.Effect{
		reaper.Secrets(dbStore, secretWriter),
		reaper.Objects(dbStore, attachments),
	}

	var overdue []string
	for _, effect := range effects {
		res, err := reaper.New(effect, logger).Sweep(ctx)
		if err != nil {
			return err
		}

		logger.Info("Swept",
			"effect", res.Effect,
			"applied", res.Applied,
			"superseded", res.Superseded,
			"failed", res.Failed,
			"outstanding", res.Outstanding,
		)

		if res.Overdue() {
			overdue = append(overdue,
				fmt.Sprintf("%s: %d outstanding, oldest %s",
					res.Effect, res.Outstanding, res.Oldest))
		}
	}

	if len(overdue) > 0 {
		return fmt.Errorf("%w: %s", reaper.ErrOverdue, joinLines(overdue))
	}
	return nil
}

func joinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "; "
		}
		out += line
	}
	return out
}
