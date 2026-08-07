package main

import (
	"io"
	"log/slog"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func newLogger(cfg *config.Config, out io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}
	if cfg.Environment == config.EnvironmentDevelopment {
		opts.AddSource = true
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}
