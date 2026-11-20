package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/purge"
	"github.com/LaplacianAI/openarity/apps/brain/internal/reaper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
)

func runWorker(ctx context.Context, cfg *config.Config, logger *slog.Logger, dbStore *store.Store) error {
	secretStore := openSecretStore(cfg, logger)

	effects, err := newEffects(ctx, cfg, logger, dbStore, secretStore)
	if err != nil {
		return err
	}

	jobs, closeJobs, err := newJobs(ctx, cfg, logger, dbStore, secretStore, effects)
	if err != nil {
		return err
	}
	defer closeJobs()

	if err := reaper.SweepAll(ctx, logger, effects...); err != nil {
		if !errors.Is(err, reaper.ErrOverdue) {
			return err
		}
		logger.ErrorContext(ctx, "Starting with an erasure already overdue", "error", err)
	}

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if err := startBrowserProxy(ctx, cfg, logger, stop); err != nil {
		return err
	}
	if err := worker.New(cfg.PostgresDSN, logger, jobs...).Run(ctx); err != nil {
		return err
	}
	if err := context.Cause(ctx); errors.Is(err, errProxyStopped) {
		return err
	}
	return nil
}

// errProxyStopped is the worker stopping because its browser proxy did: a
// worker left running without it looks healthy while every self-hosted
// browser reaches nothing, so it exits and is started again with one.
var errProxyStopped = errors.New("the browser's proxy stopped")

// startBrowserProxy serves the browser's proxy on BROWSER_PROXY_BIND, if it
// is set. It listens before the worker starts, so a bind that cannot be had
// stops the worker rather than leaving the browser with no way out.
func startBrowserProxy(ctx context.Context, cfg *config.Config, logger *slog.Logger, stop context.CancelCauseFunc) error {
	if cfg.BrowserProxyBind == "" {
		return nil
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.BrowserProxyBind)
	if err != nil {
		return fmt.Errorf("the browser's proxy cannot listen on BROWSER_PROXY_BIND %s: %w", cfg.BrowserProxyBind, err)
	}
	go serveBrowserProxy(ctx, browser.NewProxy(hub.Public, logger), l, stop)
	return nil
}

// serveBrowserProxy runs p on l, and stops the worker if it ends on its own.
func serveBrowserProxy(ctx context.Context, p *browser.Proxy, l net.Listener, stop context.CancelCauseFunc) {
	err := p.Serve(ctx, l)
	if ctx.Err() == nil {
		stop(fmt.Errorf("%w: %w", errProxyStopped, err))
	}
}

func newJobs(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, secretStore secrets.Store, effects []reaper.Effect,
) ([]worker.Job, func(), error) {
	sweep := reaper.SweepJob(logger, effects...)
	teamPurge := purge.Job(dbStore, cfg.TeamPurgeAfter, logger)
	replies, err := newReplyJob(cfg, logger, dbStore, secretStore)
	if err != nil {
		return nil, nil, err
	}

	if cfg.EmbeddingModel == "" {
		logger.WarnContext(ctx, "EMBEDDING_MODEL is not set, so the graph is not being projected or searched")
		runs, err := newRunJob(cfg, logger, dbStore, secretStore, nil)
		if err != nil {
			return nil, nil, err
		}
		return []worker.Job{sweep, teamPurge, replies, runs.SendsReplies(gateway.KickReplies, gateway.KickEvent)}, func() {}, nil
	}

	g, embedder, closeGraph, err := dialGraph(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	runs, err := newRunJob(cfg, logger, dbStore, secretStore,
		&runtime.GraphDeps{Search: g, Embed: embedder, Store: dbStore})
	if err != nil {
		closeGraph()
		return nil, nil, err
	}
	projector := graph.NewProjector(dbStore, g, embedder, logger)
	return []worker.Job{
		sweep, teamPurge, replies, runs.SendsReplies(gateway.KickReplies, gateway.KickEvent), graph.DrainJob(projector, logger),
	}, closeGraph, nil
}
