package main

import (
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/telegram"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/whatsapp"
)

func newRegistry() (gateway.Registry, error) {
	return gateway.NewRegistry(
		custom.New(),
		telegram.New(),
		whatsapp.New(),
	)
}
