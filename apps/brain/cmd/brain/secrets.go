package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const secretStoreTimeout = 5 * time.Second

func newSecretStore(cfg *config.Config, logger *slog.Logger) secrets.Store {
	if cfg.SecretsAppRoleID == "" || cfg.SecretsAppRoleSecret == "" {
		logger.Warn("no OpenBao AppRole credentials; using an in-memory secret " +
			"store, which holds nothing. Channels will not verify.")
		return secrets.Static{}
	}

	return secrets.NewOpenBao(
		cfg.SecretsAddr,
		cfg.SecretsAppRoleID,
		cfg.SecretsAppRoleSecret,
		cfg.SecretsKVMount,
		nil,
	)
}

func checkSecretStore(ctx context.Context, store secrets.Store) error {
	prober, ok := store.(secrets.Prober)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, secretStoreTimeout)
	defer cancel()

	if err := prober.Ping(ctx); err != nil {
		return fmt.Errorf("secret store unreachable at startup: %w", err)
	}
	return nil
}
