package mcpservers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	codeUniqueViolation   = "23505"
	codeRestrictViolation = "23001"
)

type Store interface {
	CreateMCPServer(ctx context.Context, arg db.CreateMCPServerParams) (db.McpServer, error)
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	ListMCPServersByTeam(ctx context.Context, arg db.ListMCPServersByTeamParams) ([]db.McpServer, error)
	UpdateMCPServer(ctx context.Context, arg db.UpdateMCPServerParams) (db.McpServer, error)
	DeleteMCPServer(ctx context.Context, id uuid.UUID) error
	ListMCPToolsByServer(ctx context.Context, mcpServerID uuid.UUID) ([]db.McpTool, error)
}

type handler struct {
	logger *slog.Logger
	store  Store
}

func New(logger *slog.Logger, s Store) *api.Router {
	h := &handler{logger: logger, store: s}

	r := api.NewRouter("/teams")
	r.Get("/{id}/mcp-servers", h.list)
	r.Post("/{id}/mcp-servers", h.create)
	r.Get("/{id}/mcp-servers/{serverID}", h.get)
	r.Put("/{id}/mcp-servers/{serverID}", h.update)
	r.Delete("/{id}/mcp-servers/{serverID}", h.delete)
	r.Get("/{id}/mcp-servers/{serverID}/tools", h.tools)

	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	f, ok := readRequest(w, r, teamID)
	if !ok {
		return
	}

	row, err := h.store.CreateMCPServer(r.Context(), db.CreateMCPServerParams{
		TeamID: teamID, Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
		AuthSecretRef: f.AuthSecretRef, Bare: f.Bare,
	})
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "an MCP server with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to create MCP server", err)
		return
	}

	h.write(w, u, http.StatusCreated, row)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	h.write(w, u, http.StatusOK, row)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	f, ok := readRequest(w, r, teamID)
	if !ok {
		return
	}

	current, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	row, err := h.store.UpdateMCPServer(r.Context(), db.UpdateMCPServerParams{
		Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
		AuthSecretRef: f.AuthSecretRef, Bare: f.Bare, ID: current.ID,
	})
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "an MCP server with that name already exists", http.StatusConflict)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to update MCP server", err)
		return
	}

	h.write(w, u, http.StatusOK, row)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	err := h.store.DeleteMCPServer(r.Context(), row.ID)
	if hasCode(err, codeRestrictViolation) {
		http.Error(w, "the MCP server is granted to an agent", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to delete MCP server", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params, ok := page(w, r, teamID, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListMCPServersByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list MCP servers", err)
		return
	}

	servers, err := viewAll(rows)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read MCP server", err)
		return
	}

	out, err := api.MapPage(servers, limit,
		func(s server) any { return serverCursor{CreatedAt: s.CreatedAt, ID: s.ID} },
		func(s server) server { return s },
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page MCP servers", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) tools(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	rows, err := h.store.ListMCPToolsByServer(r.Context(), row.ID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list MCP tools", err)
		return
	}

	out := toolList{Items: make([]tool, len(rows))}
	for i, t := range rows {
		out.Items[i] = tool{Name: t.Name, Description: t.Description, InputSchema: json.RawMessage(t.InputSchema)}
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) find(w http.ResponseWriter, r *http.Request, u *auth.User, teamID uuid.UUID) (db.McpServer, bool) {
	id, err := uuid.Parse(r.PathValue("serverID"))
	if err != nil {
		http.Error(w, "MCP server id must be a uuid", http.StatusBadRequest)
		return db.McpServer{}, false
	}

	row, err := h.store.GetMCPServer(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.TeamID != teamID) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.McpServer{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read MCP server", err)
		return db.McpServer{}, false
	}

	return row, true
}

func (h *handler) write(w http.ResponseWriter, u *auth.User, status int, row db.McpServer) {
	out, err := view(row)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read MCP server", err)
		return
	}

	api.WriteJSON(w, h.logger, status, out)
}

func readRequest(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (fields, bool) {
	var req serverRequest
	if !api.DecodeJSON(w, r, &req) {
		return fields{}, false
	}

	f, err := parse(teamID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return fields{}, false
	}

	return f, true
}

func page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListMCPServersByTeamParams, bool) {
	params := db.ListMCPServersByTeamParams{TeamID: teamID, PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c serverCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
}

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func view(row db.McpServer) (server, error) {
	var env map[string]string
	if err := json.Unmarshal(row.Env, &env); err != nil {
		return server{}, err
	}

	return server{
		ID: row.ID, TeamID: row.TeamID, Name: row.Name, URL: row.Url, Command: row.Command, Env: env,
		AuthSecretRef: row.AuthSecretRef, Bare: row.Bare, DiscoveredAt: row.DiscoveredAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func viewAll(rows []db.McpServer) ([]server, error) {
	out := make([]server, len(rows))
	for i, row := range rows {
		s, err := view(row)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}
