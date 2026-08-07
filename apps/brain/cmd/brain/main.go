package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/server"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg, out)
	logger.Info("Starting brain service",
		"environment", cfg.Environment,
		"log_level", cfg.LogLevel,
	)

	return server.New(cfg, logger).Run(ctx)
}
