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
	"github.com/LaplacianAI/openarity/apps/brain/internal/discovery"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	codeUniqueViolation   = "23505"
	codeRestrictViolation = "23001"
)

type Store interface {
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	ListMCPServersByTeam(ctx context.Context, arg db.ListMCPServersByTeamParams) ([]db.McpServer, error)
	DeleteMCPServer(ctx context.Context, id uuid.UUID) error
	GetOAuthConnection(ctx context.Context, serverID uuid.UUID) (db.McpOauthConnection, error)
	ListOAuthConnectionsOf(ctx context.Context, serverIDs []uuid.UUID) ([]db.McpOauthConnection, error)
	DeleteOAuthConnection(ctx context.Context, serverID uuid.UUID) (int64, error)
	ListMCPToolsByServer(ctx context.Context, mcpServerID uuid.UUID) ([]db.McpTool, error)
	RecordMCPServerDiscoveryFailure(ctx context.Context, arg db.RecordMCPServerDiscoveryFailureParams) (int64, error)
	InTx(ctx context.Context, fn func(Queries) error) error
}

type handler struct {
	logger    *slog.Logger
	store     Store
	refresher *Refresher
	secrets   SecretWriter
	ending    Ending
}

// Ending is what ending a sign-in takes: the tokens it holds, and the
// authorization server told it is over.
type Ending struct {
	Secrets discovery.Secrets
	Revoker Revoker
}

type Revoker interface {
	Revoke(ctx context.Context, as discovery.AuthServer, c discovery.Client, token, hint string) error
}

// SecretWriter is the one write the secrets route makes: a secret, whole.
type SecretWriter interface {
	PutAll(ctx context.Context, path string, values map[string]string) error
}

// New takes the writer its secrets route writes with; nil, and that route
// says the brain does not write secrets.
func New(logger *slog.Logger, s Store, d Discoverer, w SecretWriter, e Ending) *api.Router {
	h := &handler{logger: logger, store: s, refresher: NewRefresher(logger, d, s), secrets: w, ending: e}

	r := api.NewRouter("/teams")
	r.Get("/{id}/mcp-servers", h.list)
	r.Post("/{id}/mcp-servers", h.create)
	r.Get("/{id}/mcp-servers/{serverID}", h.get)
	r.Put("/{id}/mcp-servers/{serverID}", h.update)
	r.Delete("/{id}/mcp-servers/{serverID}", h.delete)
	r.Delete("/{id}/mcp-servers/{serverID}/oauth", h.disconnect)
	r.Get("/{id}/mcp-servers/{serverID}/tools", h.tools)
	r.Post("/{id}/mcp-servers/{serverID}/discover", h.discover)
	r.Put("/{id}/mcp-secrets/{name}", h.putSecret)

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

	var row db.McpServer
	err := h.store.InTx(r.Context(), func(q Queries) error {
		var err error
		row, err = q.CreateMCPServer(r.Context(), db.CreateMCPServerParams{
			TeamID: teamID, Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
			AuthSecretRef: f.AuthSecretRef, Bare: f.Bare, Vars: f.Vars, Egress: f.Egress,
		})
		if err != nil || f.AuthKind != authOAuth {
			return err
		}
		row, err = q.SetMCPServerAuth(r.Context(), authOf(row, f))
		return err
	})
	if hasCode(err, codeUniqueViolation) {
		http.Error(w, "an MCP server with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to create MCP server", err)
		return
	}

	h.write(w, r, u, http.StatusCreated, row)
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

	h.write(w, r, u, http.StatusOK, row)
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

	// How it is reached first, so a server switching to a static token is
	// static before it is given one; a sign-in made for another address or
	// client ends with it.
	var row db.McpServer
	err := h.store.InTx(r.Context(), func(q Queries) error {
		if _, err := q.SetMCPServerAuth(r.Context(), authOf(current, f)); err != nil {
			return err
		}
		var err error
		row, err = q.UpdateMCPServer(r.Context(), db.UpdateMCPServerParams{
			Name: f.Name, Url: f.URL, Command: f.Command, Env: f.Env,
			AuthSecretRef: f.AuthSecretRef, Bare: f.Bare, Vars: f.Vars, Egress: f.Egress, ID: current.ID,
		})
		return err
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

	h.write(w, r, u, http.StatusOK, row)
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

	ending := h.endingOf(r.Context(), row)
	err := h.store.DeleteMCPServer(r.Context(), row.ID)
	if hasCode(err, codeRestrictViolation) {
		http.Error(w, "the MCP server is granted to an agent", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to delete MCP server", err)
		return
	}
	h.revoke(r.Context(), ending)

	w.WriteHeader(http.StatusNoContent)
}

// disconnect ends a server's sign-in: here at once, and at the authorization
// server as well as it will be told. A server with none is disconnected
// already.
func (h *handler) disconnect(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}

	ending := h.endingOf(r.Context(), row)
	if _, err := h.store.DeleteOAuthConnection(r.Context(), row.ID); err != nil {
		api.Fail(w, h.logger, u, "failed to disconnect MCP server", err)
		return
	}
	h.revoke(r.Context(), ending)

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

	servers, err := h.viewAll(r.Context(), rows)
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

func (h *handler) write(w http.ResponseWriter, r *http.Request, u *auth.User, status int, row db.McpServer) {
	out, err := h.viewOne(r.Context(), row)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read MCP server", err)
		return
	}

	api.WriteJSON(w, h.logger, status, out)
}

func readRequest(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (Fields, bool) {
	var req Request
	if !api.DecodeJSON(w, r, &req) {
		return Fields{}, false
	}

	f, err := Parse(teamID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return Fields{}, false
	}

	return f, true
}

func page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListMCPServersByTeamParams, bool) {
	params := db.ListMCPServersByTeamParams{TeamID: teamID, PageSize: limit + 1}
	q, ok := api.Query(w, r)
	if !ok {
		return params, false
	}
	params.Q = q

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

// unmarshalOrEmpty reads a column the database never leaves null, and a row
// built in Go may: nothing there is nothing set.
func unmarshalOrEmpty(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func view(row db.McpServer) (server, error) {
	var env, vars map[string]string
	if err := json.Unmarshal(row.Env, &env); err != nil {
		return server{}, err
	}
	var egress []sandbox.HostPort
	if err := unmarshalOrEmpty(row.Vars, &vars); err != nil {
		return server{}, err
	}
	if err := unmarshalOrEmpty(row.Egress, &egress); err != nil {
		return server{}, err
	}

	return server{
		ID: row.ID, TeamID: row.TeamID, Name: row.Name, URL: row.Url, Command: row.Command, Env: env, Vars: vars, Egress: egress,
		AuthSecretRef: row.AuthSecretRef, Bare: row.Bare, DiscoveredAt: row.DiscoveredAt,
		AuthKind: row.AuthKind, OAuthClientID: row.OauthClientID, OAuthClientSecret: row.OauthClientSecretRef, OAuthScopes: row.OauthScopes,
		LastDiscoveryError: row.LastDiscoveryError, LastDiscoveryAt: row.LastDiscoveryAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (h *handler) viewOne(ctx context.Context, row db.McpServer) (server, error) {
	out, err := h.viewAll(ctx, []db.McpServer{row})
	if err != nil {
		return server{}, err
	}
	return out[0], nil
}

// viewAll reads the page's sign-ins in one query: only a server that signs in
// can have one, so a page of static servers asks for none.
func (h *handler) viewAll(ctx context.Context, rows []db.McpServer) ([]server, error) {
	var signing []uuid.UUID
	for _, row := range rows {
		if row.AuthKind == authOAuth {
			signing = append(signing, row.ID)
		}
	}
	byServer := map[uuid.UUID]db.McpOauthConnection{}
	if len(signing) > 0 {
		conns, err := h.store.ListOAuthConnectionsOf(ctx, signing)
		if err != nil {
			return nil, err
		}
		for _, c := range conns {
			byServer[c.ServerID] = c
		}
	}

	out := make([]server, len(rows))
	for i, row := range rows {
		s, err := view(row)
		if err != nil {
			return nil, err
		}
		if c, ok := byServer[row.ID]; ok {
			s.Connection = &connection{
				Status: c.Status, Reason: c.Reason, ConnectedBy: c.ConnectedBy, ConnectedAt: c.ConnectedAt,
				ScopesGranted: c.ScopesGranted, AccessExpiresAt: c.AccessExpiresAt,
			}
		}
		out[i] = s
	}
	return out, nil
}

func authOf(row db.McpServer, f Fields) db.SetMCPServerAuthParams {
	p := db.SetMCPServerAuthParams{ID: row.ID, TeamID: row.TeamID, Url: f.URL, AuthKind: f.AuthKind, AuthSecretRef: f.AuthSecretRef}
	if f.AuthKind == authOAuth {
		p.ClientID, p.ClientSecretRef, p.Scopes = f.ClientID, f.ClientSecretRef, f.Scopes
	}
	return p
}
