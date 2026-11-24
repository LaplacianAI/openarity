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
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/mcpregistry"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/stores/postgres"
)

// runDeps are what a run is built and run with, made once a worker: the
// runs and the replays share them, and so one keeper of the team's rooms.
type runDeps struct {
	attachments *objects.Encrypted
	clients     agent.ClientFactory
	runner      *agent.Runner
	search      *runtime.SearchDeps
	fetch       *runtime.FetchDeps
	searchHTTP  *http.Client
	discoverer  *discovery.Discoverer
	rooms       *keeper.Keeper
	endpoint    agent.Endpoint
}

func newRunDeps(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, secretStore secrets.Store) (*runDeps, error) {
	attachments, err := newAttachmentStore(cfg, secretStore, logger)
	if err != nil {
		return nil, err
	}
	// Each request carries the bytes of the files its transcript names.
	clients := runtime.Resolving(openaicompat.Factory(), dbStore, attachments)
	runner, err := agent.New(clients, runtime.Patterns()...)
	if err != nil {
		return nil, fmt.Errorf("building the agent runner: %w", err)
	}
	searchClient := websearch.NewClient(reachable(cfg.MCPPrivateNetworks))
	discoverer := newDiscoverer(cfg, secretStore, dbStore)
	return &runDeps{
		attachments: attachments, clients: clients, runner: runner,
		search:     &runtime.SearchDeps{Secrets: secretStore, New: newSearchers(searchClient)},
		fetch:      newFetch(searchClient, net.DefaultResolver),
		searchHTTP: searchClient, discoverer: discoverer,
		rooms:    runCommandsInRooms(discoverer, cfg, logger, dbStore, secretStore),
		endpoint: agent.Endpoint{BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey},
	}, nil
}

func newRunJob(
	cfg *config.Config, logger *slog.Logger,
	dbStore *store.Store, secretStore secrets.Store, graphDeps *runtime.GraphDeps, d *runDeps,
) (*runtime.Job, error) {
	registry, err := newRegistry()
	if err != nil {
		return nil, err
	}
	builder := runtime.NewBuilder(dbStore, d.attachments, d.discoverer, graphDeps, d.search, d.fetch).
		WithWorkshop(workshop{blueprints.NewWorkshop(blueprintStore{dbStore}, d.discoverer, runtime.Loops())}).
		WithApprovals(dbStore).
		// A tool's file is kept as its conversation's attachment, sealed
		// as a person's is, so a transcript never holds its bytes.
		WithToolFiles(runtime.ToolFiles{Store: dbStore, Objects: d.attachments, Logger: logger}).
		// A top-level run hands its person a file where its conversation can
		// take one: the dashboard's, or a channel whose provider sends files.
		WithFileSends(runtime.FileSends{Store: dbStore, SendsFiles: registry.SendsFiles}).
		WithBrowsers(newBrowsers(secretStore, d.searchHTTP, reachable(cfg.MCPPrivateNetworks), net.DefaultResolver))
	if reg := newMCPRegistry(cfg); reg != nil {
		builder.WithRegistry(reg)
	}
	if ws := newWorkspaces(d.rooms, cfg); ws != nil {
		builder.WithWorkspaces(ws)
	}
	streamer, err := agent.New(d.clients, runtime.StreamingPatterns()...)
	if err != nil {
		return nil, fmt.Errorf("building the streaming agent runner: %w", err)
	}
	executor := runtime.NewExecutor(dbStore, builder, d.runner, d.endpoint, postgres.New(dbStore.Pool()), logger).
		WithStreaming(streamer)

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
