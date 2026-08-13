package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/authconfig"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/docs"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teams"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/whoami"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func newRouters(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, authorizer *authz.Authorizer) []server.Router {
	routers := []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, authorizer),
		authconfig.New(logger, cfg),
	}

	if cfg.Environment == config.EnvironmentDevelopment {
		routers = append(routers, docs.New(logger))
	}

	return routers
}
