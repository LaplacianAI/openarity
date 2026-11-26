package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/graph"
	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/purge"
	"github.com/LaplacianAI/openarity/apps/brain/internal/reaper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/replay"
	"github.com/LaplacianAI/openarity/apps/brain/internal/retention"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill/hub"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
	"github.com/LaplacianAI/openarity/apps/brain/internal/worker"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
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
	// The proxy is not waited for: its tunnels end with ctx, cut mid-page
	// rather than drained, as the browser's run is being stopped anyway.
	return workerError(ctx, worker.New(cfg.PostgresDSN, logger, jobs...).Run(ctx))
}

// workerError is what the worker ended with, ran, and why it was stopped if
// that was its proxy: a worker that stopped cleanly because the proxy died
// still exits as failed, so it is started again.
func workerError(ctx context.Context, ran error) error {
	if cause := context.Cause(ctx); errors.Is(cause, errProxyStopped) {
		return errors.Join(cause, ran)
	}
	return ran
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
		stop(errors.Join(errProxyStopped, err))
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
	judging := newImproveJob(cfg, logger, dbStore)
	templates := keeper.TemplatesJob(newTemplates(cfg, logger, dbStore, secretStore))
	watching := improve.NewWatchJob(improve.NewWatcher(dbStore, logger))
	scheduling := triggers.NewJob(dbStore, runtime.EnqueueRun, logger)
	retaining := retention.NewJob(logger, triggers.FireWorkflow)
	deps, err := newRunDeps(cfg, logger, dbStore, secretStore)
	if err != nil {
		return nil, nil, err
	}

	if cfg.EmbeddingModel == "" {
		logger.WarnContext(ctx, "EMBEDDING_MODEL is not set, so the graph is not being projected or searched")
		runs, err := newRunJob(cfg, logger, dbStore, secretStore, nil, deps)
		if err != nil {
			return nil, nil, err
		}
		improving, replaying, err := newImprovingJobs(cfg, logger, dbStore, secretStore, nil, deps)
		if err != nil {
			return nil, nil, err
		}
		return []worker.Job{sweep, teamPurge, replies, runs.SendsReplies(gateway.KickReplies, gateway.KickEvent, gateway.KickFile), judging, improving, replaying, watching, scheduling, retaining, templates}, func() {}, nil
	}

	g, embedder, closeGraph, err := dialGraph(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	graphDeps := &runtime.GraphDeps{Search: g, Embed: embedder, Store: dbStore}
	runs, err := newRunJob(cfg, logger, dbStore, secretStore, graphDeps, deps)
	if err != nil {
		closeGraph()
		return nil, nil, err
	}
	improving, replaying, err := newImprovingJobs(cfg, logger, dbStore, secretStore, graphDeps, deps)
	if err != nil {
		closeGraph()
		return nil, nil, err
	}
	projector := graph.NewProjector(dbStore, g, embedder, logger)
	return []worker.Job{
		sweep, teamPurge, replies, runs.SendsReplies(gateway.KickReplies, gateway.KickEvent, gateway.KickFile), graph.DrainJob(projector, logger), judging, improving, replaying, watching, scheduling, retaining, templates,
	}, closeGraph, nil
}

// newImprovingJobs is the improver and the replays of what it proposes: each
// proposal it stores is tried on past runs, when its team has that on. A
// proposal whose tries cannot be queued is still stored; it is shown without
// them.
func newImprovingJobs(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store,
	graphDeps *runtime.GraphDeps, deps *runDeps,
) (*improve.ImproverJob, *replay.Job, error) {
	replaying := newReplayJob(logger, dbStore, graphDeps, deps)
	improving, err := newImproverJob(cfg, logger, dbStore, secretStore, func(ctx context.Context, proposalID, teamID uuid.UUID) {
		if err := replaying.Enqueue(ctx, proposalID, teamID); err != nil {
			logger.WarnContext(ctx, "a proposal's tries on past runs could not be queued",
				slog.String("proposal_id", proposalID.String()), slog.Any("error", err))
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return improving, replaying, nil
}

// newImproverJob finds each agent's patterns and runs the improver on them,
// on the judge's model endpoint; it reads skill files from the store the
// runs read them from.
func newImproverJob(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store,
	stored func(ctx context.Context, proposalID, teamID uuid.UUID),
) (*improve.ImproverJob, error) {
	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return nil, err
	}
	judge := improve.NewJudge(openaicompat.Factory(), agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey})
	im, err := improve.NewImprover(dbStore, attachments, judge, runtime.Loops(), logger)
	if err != nil {
		return nil, err
	}
	im.OnStored(stored)
	return improve.NewImproverJob(im, dbStore), nil
}

// newImproveJob judges a team's finished runs and digests them, on the same
// model endpoint the runs use.
func newImproveJob(cfg *config.Config, logger *slog.Logger, dbStore *store.Store) *improve.Job {
	judge := improve.NewJudge(openaicompat.Factory(), agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey})
	return improve.NewJob(improve.NewChecker(dbStore, judge, logger), dbStore)
}
