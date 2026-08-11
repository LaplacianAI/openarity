package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/teams"
	"github.com/LaplacianAI/openarity/apps/brain/internal/api/whoami"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func newRouters(logger *slog.Logger, dbStore *store.Store, authorizer *authz.Authorizer) []server.Router {
	return []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, authorizer),
	}
}
