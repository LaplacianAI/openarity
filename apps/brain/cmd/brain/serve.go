package main

import (
	"context"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
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

	warnIfIssuerIsNew(ctx, cfg, logger, dbStore)

	return server.New(
		cfg,
		logger,
		server.Deps{
			Checks:   []server.Check{{Name: "postgres", Pinger: dbStore}},
			Verifier: verifier,
			Resolver: dbStore,
		},
		routers...,
	).Run(ctx)
}
