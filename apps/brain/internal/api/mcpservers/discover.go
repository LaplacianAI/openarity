package mcpservers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ErrChanged is a server edited while it was discovered: its tools are kept,
// and it is discovered again.
var ErrChanged = errors.New("the server changed while it was being discovered")

type Discoverer interface {
	Discover(ctx context.Context, s db.McpServer) ([]discovery.Tool, error)
}

type Queries interface {
	MarkMCPServerDiscovered(ctx context.Context, arg db.MarkMCPServerDiscoveredParams) (int64, error)
	UpsertMCPTool(ctx context.Context, arg db.UpsertMCPToolParams) error
	DeleteMCPToolsNotIn(ctx context.Context, arg db.DeleteMCPToolsNotInParams) error
}

// RefreshStore is what recording a discovery takes: the tools in one
// transaction, or the reason it failed.
type RefreshStore interface {
	RecordMCPServerDiscoveryFailure(ctx context.Context, arg db.RecordMCPServerDiscoveryFailureParams) (int64, error)
	InTx(ctx context.Context, fn func(Queries) error) error
}

// Refresher asks a server for its tools and records what came of it, for the
// discover route and for a blueprint applied.
type Refresher struct {
	logger     *slog.Logger
	discoverer Discoverer
	store      RefreshStore
}

func NewRefresher(logger *slog.Logger, d Discoverer, s RefreshStore) *Refresher {
	return &Refresher{logger: logger, discoverer: d, store: s}
}

// Failure is a discovery that failed for a reason the caller may read, and
// which was recorded on the server. Any other error is the brain's.
type Failure struct {
	Message string
	Status  int
}

func (f *Failure) Error() string { return f.Message }

// Refresh discovers a server's tools and records them, replacing its last
// ones. It returns a *Failure for the server's fault, ErrChanged when the
// server moved meanwhile, and any other error for the brain's.
func (rf *Refresher) Refresh(ctx context.Context, row db.McpServer) ([]discovery.Tool, error) {
	tools, err := rf.discoverer.Discover(ctx, row)
	if err != nil {
		f, ours := rf.failure(row, err)
		if ours {
			return nil, err
		}
		rf.record(ctx, row, f)
		return nil, f
	}

	err = rf.store.InTx(ctx, func(q Queries) error { return record(ctx, q, row, tools) })
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// failure is what a discovery error tells the caller; ours is the brain's own
// failure, which is not recorded against the server.
func (rf *Refresher) failure(row db.McpServer, err error) (f *Failure, ours bool) {
	switch {
	case errors.Is(err, discovery.ErrCommandServer):
		return &Failure{discovery.ErrCommandServer.Error(), http.StatusConflict}, false
	case errors.Is(err, context.DeadlineExceeded):
		return &Failure{"the server did not answer in time", http.StatusGatewayTimeout}, false
	case errors.Is(err, discovery.ErrRefused):
		rf.logger.Warn("MCP discovery refused", "server", row.ID, "error", err)
		return &Failure{
			"the brain may not reach that server: its address is private or link-local, " +
				"it redirected, or it needs a token over plain http", http.StatusUnprocessableEntity,
		}, false
	case errors.Is(err, discovery.ErrUnusable):
		return &Failure{err.Error(), http.StatusUnprocessableEntity}, false
	case errors.Is(err, secrets.ErrNotFound):
		return &Failure{"auth_secret_ref names a secret that is not in the secret store, or is empty", http.StatusUnprocessableEntity}, false
	case errors.Is(err, secrets.ErrUnavailable):
		return nil, true
	default:
		rf.logger.Warn("MCP discovery failed", "server", row.ID, "error", err)
		return &Failure{"the server did not answer as an MCP server", http.StatusBadGateway}, false
	}
}

func (rf *Refresher) record(ctx context.Context, row db.McpServer, f *Failure) {
	_, err := rf.store.RecordMCPServerDiscoveryFailure(ctx, db.RecordMCPServerDiscoveryFailureParams{
		Error: f.Message, ID: row.ID, UpdatedAt: row.UpdatedAt,
	})
	if err != nil {
		rf.logger.Error("failed to record the MCP discovery error", "server", row.ID, "error", err)
	}
}

func (h *handler) discover(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	tools, err := h.refresher.Refresh(r.Context(), row)
	var f *Failure
	switch {
	case errors.As(err, &f):
		http.Error(w, f.Message, f.Status)
		return
	case errors.Is(err, ErrChanged):
		http.Error(w, "the server changed while it was being discovered; discover it again", http.StatusConflict)
		return
	case errors.Is(err, secrets.ErrUnavailable):
		api.Fail(w, h.logger, u, "failed to read the MCP server's token", err)
		return
	case err != nil:
		api.Fail(w, h.logger, u, "failed to record MCP tools", err)
		return
	}

	out := toolList{Items: make([]tool, len(tools))}
	for i, t := range tools {
		out.Items[i] = tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func record(ctx context.Context, q Queries, row db.McpServer, tools []discovery.Tool) error {
	n, err := q.MarkMCPServerDiscovered(ctx, db.MarkMCPServerDiscoveredParams{ID: row.ID, UpdatedAt: row.UpdatedAt})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrChanged
	}

	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
		err := q.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
			McpServerID: row.ID, TeamID: row.TeamID, Name: t.Name,
			Description: t.Description, InputSchema: t.InputSchema,
		})
		if err != nil {
			return err
		}
	}

	return q.DeleteMCPToolsNotIn(ctx, db.DeleteMCPToolsNotInParams{McpServerID: row.ID, Names: names})
}
