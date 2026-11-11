package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api/blueprints"
	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/mcpregistry"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
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
	discoverer := newDiscoverer(cfg, secretStore)
	builder := runtime.NewBuilder(dbStore, attachments, discoverer, graphDeps, search,
		newFetch(searchClient, net.DefaultResolver)).
		WithWorkshop(workshop{blueprints.NewWorkshop(blueprintStore{dbStore}, discoverer, runtime.Loops())})
	if reg := newMCPRegistry(cfg); reg != nil {
		builder.WithRegistry(reg)
	}
	endpoint := agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey}
	executor := runtime.NewExecutor(dbStore, builder, runner, endpoint, postgres.New(dbStore.Pool()), logger)

	return runtime.RunJob(executor, dbStore, logger), nil
}

// workshop is the blueprints' Workshop as the runtime takes it: the runtime
// does not learn the API's types.
type workshop struct{ ws *blueprints.Workshop }

func (w workshop) Team(ctx context.Context, teamID uuid.UUID) (string, error) {
	return w.ws.Team(ctx, teamID)
}

func (w workshop) ToolsOf(ctx context.Context, teamID uuid.UUID, url string, authSecret *string) ([]runtime.ServerTool, error) {
	tools, err := w.ws.ToolsOf(ctx, teamID, url, authSecret)
	out := make([]runtime.ServerTool, len(tools))
	for i, t := range tools {
		out[i] = runtime.ServerTool{Name: t.Name, Description: t.Description}
	}
	return out, err
}

// newMCPRegistry is where the builder looks MCP servers up, reached over the
// same public-only dial as web search; nil when the operator turned it off.
func newMCPRegistry(cfg *config.Config) *mcpregistry.Client {
	if cfg.MCPRegistryURL == config.NoMCPRegistry {
		return nil
	}
	return mcpregistry.New(cfg.MCPRegistryURL, &http.Client{
		Timeout:   registryTimeout,
		Transport: websearch.Transport(reachable(cfg.MCPPrivateNetworks), "the MCP registry"),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the MCP registry may not redirect")
		},
	})
}

const registryTimeout = 15 * time.Second

func (w workshop) Propose(ctx context.Context, run db.Run, bp json.RawMessage, summary string) (runtime.Proposal, error) {
	p, err := w.ws.Propose(ctx, run, bp, summary)
	return runtime.Proposal{ID: p.ID, Report: p.Report, Changes: p.Changes, Refusals: p.Refusals}, err
}
