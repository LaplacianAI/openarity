package main

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/improve"
	"github.com/LaplacianAI/openarity/apps/brain/internal/replay"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

// newReplayJob tries each stored proposal on past runs, when its team has
// that on: built as runs are, on the endpoint they use, with nothing that
// acts on the world, and judged by the judge.
func newReplayJob(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store,
	graphDeps *runtime.GraphDeps,
) (*replay.Job, error) {
	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return nil, err
	}
	clients := runtime.Resolving(openaicompat.Factory(), dbStore, attachments)
	runner, err := agent.New(clients, runtime.Patterns()...)
	if err != nil {
		return nil, fmt.Errorf("building the replay runner: %w", err)
	}
	searchClient := websearch.NewClient(reachable(cfg.MCPPrivateNetworks))
	search := &runtime.SearchDeps{Secrets: secretStore, New: newSearchers(searchClient)}
	discoverer := newDiscoverer(cfg, secretStore, dbStore)
	runCommandsInRooms(discoverer, cfg, logger, dbStore, secretStore)
	endpoint := agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey}
	replayer := runtime.NewReplayer(attachments, discoverer, graphDeps, search,
		newFetch(searchClient, net.DefaultResolver), runner, endpoint)
	judge := improve.NewChecker(dbStore, improve.NewJudge(openaicompat.Factory(), endpoint), logger)
	return replay.NewJob(replay.New(dbStore, postgres.New(dbStore.Pool()), replayer, judge)), nil
}
