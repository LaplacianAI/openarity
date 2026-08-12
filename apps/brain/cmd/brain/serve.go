package main

import (
	"context"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
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

	authorizer := authz.New(dbStore, cfg.SuperAdmins)
	routers := newRouters(cfg, logger, dbStore, authorizer)

	// The secret store and channel map stay empty until Vault and the
	// channels table exist, so every webhook is rejected — fail closed, not
	// broken. The sink stands in for the orchestrator.
	gw := gateway.New(logger, gateway.Telegram{}, map[string]string{}, secrets.Static{}, logSink{logger: logger})

	return server.New(
		cfg,
		logger,
		server.Deps{
			DB:       dbStore,
			Verifier: verifier,
			Resolver: dbStore,
			Gateway:  gw,
		},
		routers...,
	).Run(ctx)
}
