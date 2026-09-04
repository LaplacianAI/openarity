package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
)

func main() {
	if err := run(context.Background(), os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, err := parse(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg, out)
	logger.Info("brain starting",
		"command", cmd.name,
		"environment", cfg.Environment,
	)

	// Every command shares one store, so migrate and reap carry the option
	// too. Neither resolves a principal, so neither can promote anybody — the
	// policy only has an effect on the path that authenticates a request.
	db, err := store.New(ctx, cfg.PostgresDSN,
		store.WithFirstUserBootstrap(firstUserBootstrap(cfg)),
	)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}
	logger.Info("Database connection established")

	return execute(ctx, cfg, logger, db, cmd)
}

func execute(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store, cmd command) error {
	switch cmd.name {
	case commandServe:
		return serve(ctx, cfg, logger, dbStore)
	case commandMigrate:
		return migrate(ctx, logger, dbStore, cmd.direction)
	case commandReap:
		return reap(ctx, cfg, logger, dbStore)
	case commandWorker:
		return runWorker(ctx, cfg, logger, dbStore)
	default:
		return fmt.Errorf("unhandled command %q", cmd.name)
	}
}
