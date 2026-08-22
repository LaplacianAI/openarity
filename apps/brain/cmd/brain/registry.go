package main

import (
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
)

func newRegistry() (gateway.Registry, error) {
	return gateway.NewRegistry(
		custom.New(),
	)
}
