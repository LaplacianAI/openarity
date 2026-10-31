package main

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

func newRunJob(
	cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, secretStore secrets.Store, graphDeps *runtime.GraphDeps,
) (*runtime.Job, error) {
	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return nil, err
	}

	runner, err := agent.New(openaicompat.Factory(), runtime.Patterns()...)
	if err != nil {
		return nil, fmt.Errorf("building the agent runner: %w", err)
	}

	searchClient := websearch.NewClient(reachable(cfg.MCPPrivateNetworks))
	search := &runtime.SearchDeps{Secrets: secretStore, New: newSearchers(searchClient)}
	builder := runtime.NewBuilder(dbStore, attachments, newDiscoverer(cfg, secretStore), graphDeps, search,
		newFetch(searchClient, net.DefaultResolver))
	endpoint := agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey}
	executor := runtime.NewExecutor(dbStore, builder, runner, endpoint, postgres.New(dbStore.Pool()), logger)

	return runtime.RunJob(executor, dbStore, logger), nil
}
