package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/authconfig"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/channels"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/docs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teams"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/users"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/whoami"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func newRouters(
	cfg *config.Config,
	logger *slog.Logger,
	dbStore *store.Store,
	authorizer *authz.Authorizer,
	secretWriter secrets.Writer,
	registry gateway.Registry,
) []server.Router {
	routers := []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, authorizer),
		channels.New(logger, dbStore, secretWriter, registry),
		users.New(logger, dbStore),
		authconfig.New(logger, cfg),
	}

	if cfg.Environment == config.EnvironmentDevelopment {
		routers = append(routers, docs.New(logger))
	}

	return routers
}
