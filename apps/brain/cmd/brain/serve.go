package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func newGuard(
	ctx context.Context, logger *slog.Logger,
	dbStore *store.Store, authorizer *authz.Authorizer,
) (*api.Guard, error) {
	rows, err := dbStore.ListRoutePermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("read route permissions: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("no route permissions in the database — run `brain migrate up`")
	}

	routes := authz.NewRoutes()
	for _, row := range rows {
		if err := routes.Add(row.Method, row.Path, row.Scope, row.Permission); err != nil {
			return nil, err
		}
	}

	return api.NewGuard(routes, authorizer, logger), nil
}

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

	guard, err := newGuard(ctx, logger, dbStore, authorizer)
	if err != nil {
		return err
	}

	checks := []server.Check{{Name: "postgres", Pinger: dbStore}}
	if p, ok := secretStore.(secrets.Prober); ok {
		checks = append(checks, server.Check{Name: "openbao", Pinger: p})
	}

	warnIfIssuerIsNew(ctx, cfg, logger, dbStore)

	srv := server.New(
		cfg,
		logger,
		server.Deps{
			Checks:   checks,
			Verifier: verifier,
			Resolver: dbStore,
			Guard:    guard,
		},
		routers...,
	)

	if unused := guard.Unused(); len(unused) > 0 {
		return fmt.Errorf("rbac.json maps routes this server does not serve: %s",
			strings.Join(unused, ", "))
	}

	return srv.Run(ctx)
}
