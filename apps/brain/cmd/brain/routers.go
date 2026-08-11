package main

import (
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/whoami"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
)

func newRouters(logger *slog.Logger) []server.Router {
	return []server.Router{
		whoami.New(logger),
	}
}
