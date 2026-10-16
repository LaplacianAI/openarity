package mcpservers

import (
	"context"
	"errors"
	"net/http"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var errChanged = errors.New("the server changed while it was being discovered")

type Discoverer interface {
	Discover(ctx context.Context, s db.McpServer) ([]discovery.Tool, error)
}

type Queries interface {
	MarkMCPServerDiscovered(ctx context.Context, arg db.MarkMCPServerDiscoveredParams) (int64, error)
	UpsertMCPTool(ctx context.Context, arg db.UpsertMCPToolParams) error
	DeleteMCPToolsNotIn(ctx context.Context, arg db.DeleteMCPToolsNotInParams) error
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

	tools, err := h.discoverer.Discover(r.Context(), row)
	if err != nil {
		h.discoveryFailed(w, u, row, err)
		return
	}

	err = h.store.InTx(r.Context(), func(q Queries) error { return record(r.Context(), q, row, tools) })
	if errors.Is(err, errChanged) {
		http.Error(w, "the server changed while it was being discovered; discover it again", http.StatusConflict)
		return
	}
	if err != nil {
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
		return errChanged
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

func (h *handler) discoveryFailed(w http.ResponseWriter, u *auth.User, row db.McpServer, err error) {
	switch {
	case errors.Is(err, discovery.ErrCommandServer):
		http.Error(w, discovery.ErrCommandServer.Error(), http.StatusConflict)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "the server did not answer in time", http.StatusGatewayTimeout)
	case errors.Is(err, discovery.ErrRefused):
		h.logger.Warn("MCP discovery refused", "server", row.ID, "error", err)
		http.Error(w, "the brain may not reach that server: its address is private or link-local, "+
			"it redirected, or it needs a token over plain http", http.StatusUnprocessableEntity)
	case errors.Is(err, discovery.ErrUnusable):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, secrets.ErrNotFound):
		http.Error(w, "auth_secret_ref names a secret that is not in the secret store, or is empty", http.StatusUnprocessableEntity)
	case errors.Is(err, secrets.ErrUnavailable):
		api.Fail(w, h.logger, u, "failed to read the MCP server's token", err)
	default:
		h.logger.Warn("MCP discovery failed", "server", row.ID, "error", err)
		http.Error(w, "the server did not answer as an MCP server", http.StatusBadGateway)
	}
}
