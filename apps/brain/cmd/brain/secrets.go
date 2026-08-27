package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/openbao"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
)

const secretStoreTimeout = 5 * time.Second

func newSecretStore(cfg *config.Config, logger *slog.Logger) secrets.Store {
	switch cfg.SecretsBackend {
	case config.SecretsBackendOpenBao:
		return openbao.New(
			cfg.SecretsAddr,
			cfg.SecretsAppRoleID,
			cfg.SecretsAppRoleSecret,
			cfg.SecretsKVMount,
			nil,
		)

	case config.SecretsBackendStatic:
		logger.Warn("SECRETS_BACKEND=static: secrets are held in this process " +
			"and lost on restart. Channels will not verify.")
		return static.New()
	}

	return static.New()
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
