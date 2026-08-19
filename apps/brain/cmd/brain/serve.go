package main

import (
	"context"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	logger.Info("Starting brain service",
		"environment", cfg.Environment,
		"log_level", cfg.LogLevel,
	)

	verifier, err := newVerifier(ctx, cfg)
	if err != nil {
		return err
	}

	secretStore := newSecretStore(cfg, logger)
	if err := checkSecretStore(ctx, secretStore); err != nil {
		return err
	}

	authorizer := authz.New(dbStore, cfg.SuperAdmins)
	routers := newRouters(cfg, logger, dbStore, authorizer)

	checks := []server.Check{{Name: "postgres", Pinger: dbStore}}
	if p, ok := secretStore.(secrets.Prober); ok {
		checks = append(checks, server.Check{Name: "openbao", Pinger: p})
	}

	warnIfIssuerIsNew(ctx, cfg, logger, dbStore)

	return server.New(
		cfg,
		logger,
		server.Deps{
			Checks:   checks,
			Verifier: verifier,
			Resolver: dbStore,
		},
		routers...,
	).Run(ctx)
}
