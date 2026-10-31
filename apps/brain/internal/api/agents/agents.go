package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const codeUniqueViolation = "23505"

var grantConstraints = []string{"agent_mcp_servers_server_in_team", "agent_skills_skill_in_team"}

type Store interface {
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	ListAgentsByTeam(ctx context.Context, arg db.ListAgentsByTeamParams) ([]db.Agent, error)
	ListAgentMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.AgentMcpServer, error)
	ListAgentSkills(ctx context.Context, agentID uuid.UUID) ([]uuid.UUID, error)
	ListAgentDelegates(ctx context.Context, agentID uuid.UUID) ([]uuid.UUID, error)
	DeleteAgent(ctx context.Context, id uuid.UUID) error
	InTx(ctx context.Context, fn func(Queries) error) error
}

type Queries interface {
	CreateAgent(ctx context.Context, arg db.CreateAgentParams) (db.Agent, error)
	UpdateAgent(ctx context.Context, arg db.UpdateAgentParams) (db.Agent, error)
	AddAgentMCPServer(ctx context.Context, arg db.AddAgentMCPServerParams) error
	ClearAgentMCPServers(ctx context.Context, agentID uuid.UUID) error
	AddAgentSkills(ctx context.Context, arg db.AddAgentSkillsParams) error
	ClearAgentSkills(ctx context.Context, agentID uuid.UUID) error
	AddAgentDelegates(ctx context.Context, arg db.AddAgentDelegatesParams) error
	ClearAgentDelegates(ctx context.Context, agentID uuid.UUID) error
}

type handler struct {
	logger *slog.Logger
	store  Store
	loops  []string
}

type grants struct {
	servers   []serverGrant
	skills    []uuid.UUID
	delegates []uuid.UUID
}

func New(logger *slog.Logger, s Store, loops []string) *api.Router {
	h := &handler{logger: logger, store: s, loops: loops}

	r := api.NewRouter("/teams")
	r.Get("/{id}/agents", h.list)
	r.Post("/{id}/agents", h.create)
	r.Get("/{id}/agents/{agentID}", h.get)
	r.Put("/{id}/agents/{agentID}", h.replace)
	r.Delete("/{id}/agents/{agentID}", h.delete)

	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	req, ok := readRequest(w, r, h.loops)
	if !ok {
		return
	}
	if req.Kind != "" && req.Kind != KindCustom {
		http.Error(w, "kind must be custom: the other kinds are agents the brain provides", http.StatusBadRequest)
		return
	}

	params, err := columns(req)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to encode agent", err)
		return
	}
	params.TeamID, params.Kind = teamID, string(KindCustom)

	var row db.Agent
	err = h.store.InTx(r.Context(), func(q Queries) error {
		var err error
		if row, err = q.CreateAgent(r.Context(), params); err != nil {
			return err
		}
		return grant(r.Context(), q, row, req)
	})
	if h.writeFailed(w, u, err) {
		return
	}

	h.write(w, u, http.StatusCreated, row, requested(req))
}

func (h *handler) replace(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	req, ok := readRequest(w, r, h.loops)
	if !ok {
		return
	}

	current, ok := h.find(w, r, u, teamID)
	if !ok {
		return
	}
	if req.Kind != "" && string(req.Kind) != current.Kind {
		http.Error(w, "kind cannot change: this agent is "+current.Kind, http.StatusBadRequest)
		return
	}
	if slices.Contains(req.Delegates, current.ID) {
		http.Error(w, "an agent cannot list itself", http.StatusBadRequest)
		return
	}

	params, err := columns(req)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to encode agent", err)
		return
	}

	var row db.Agent
	err = h.store.InTx(r.Context(), func(q Queries) error {
		var err error
		if row, err = q.UpdateAgent(r.Context(), updateParams(current.ID, params)); err != nil {
			return err
		}
		if err := q.ClearAgentMCPServers(r.Context(), row.ID); err != nil {
			return err
		}
		if err := q.ClearAgentSkills(r.Context(), row.ID); err != nil {
			return err
		}
		if err := q.ClearAgentDelegates(r.Context(), row.ID); err != nil {
			return err
		}
		return grant(r.Context(), q, row, req)
	})
	if h.writeFailed(w, u, err) {
		return
	}

	h.write(w, u, http.StatusOK, row, requested(req))
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

	servers, err := h.store.ListAgentMCPServers(r.Context(), row.ID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list agent servers", err)
		return
	}
	skills, err := h.store.ListAgentSkills(r.Context(), row.ID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list agent skills", err)
		return
	}
	delegates, err := h.store.ListAgentDelegates(r.Context(), row.ID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list agent delegates", err)
		return
	}

	h.write(w, u, http.StatusOK, row, stored(servers, skills, delegates))
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

	if err := h.store.DeleteAgent(r.Context(), row.ID); err != nil {
		api.Fail(w, h.logger, u, "failed to delete agent", err)
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

	rows, err := h.store.ListAgentsByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list agents", err)
		return
	}

	out, err := api.MapPage(rows, limit,
		func(row db.Agent) any { return agentCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		viewSummary,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page agents", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) find(w http.ResponseWriter, r *http.Request, u *auth.User, teamID uuid.UUID) (db.Agent, bool) {
	id, err := uuid.Parse(r.PathValue("agentID"))
	if err != nil {
		http.Error(w, "agent id must be a uuid", http.StatusBadRequest)
		return db.Agent{}, false
	}

	row, err := h.store.GetAgent(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.TeamID != teamID) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Agent{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read agent", err)
		return db.Agent{}, false
	}

	return row, true
}

func (h *handler) writeFailed(w http.ResponseWriter, u *auth.User, err error) bool {
	if err == nil {
		return false
	}

	var pgErr *pgconn.PgError
	isPG := errors.As(err, &pgErr)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	case isPG && pgErr.Code == codeUniqueViolation:
		http.Error(w, "an agent with that name already exists", http.StatusConflict)
	case isPG && pgErr.ConstraintName == "agent_delegates_delegate_in_team":
		http.Error(w, "a listed delegate is not an agent in this team", http.StatusBadRequest)
	case isPG && pgErr.ConstraintName == "agent_delegates_not_self":
		http.Error(w, "an agent cannot list itself", http.StatusBadRequest)
	case isPG && slices.Contains(grantConstraints, pgErr.ConstraintName):
		http.Error(w, "a granted server or skill does not exist in this team", http.StatusBadRequest)
	default:
		api.Fail(w, h.logger, u, "failed to write agent", err)
	}
	return true
}

func (h *handler) write(w http.ResponseWriter, u *auth.User, status int, row db.Agent, g grants) {
	out, err := view(row, g)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read agent", err)
		return
	}

	api.WriteJSON(w, h.logger, status, out)
}

func readRequest(w http.ResponseWriter, r *http.Request, loops []string) (agentRequest, bool) {
	var req agentRequest
	if !api.DecodeJSON(w, r, &req) {
		return agentRequest{}, false
	}

	if err := validate(req, loops); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return agentRequest{}, false
	}

	return req, true
}

func columns(req agentRequest) (db.CreateAgentParams, error) {
	p := db.CreateAgentParams{
		Name: req.Name, Description: req.Description, Instructions: req.Instructions,
		ModelName: req.Model.Name, MaxTokens: req.Model.MaxTokens, Temperature: req.Model.Temperature,
		Pattern: req.Pattern, MaxSteps: req.MaxSteps, SteerContinuations: req.SteerContinuations,
		Parser: req.Parser, TokenBudget: req.TokenBudget, Delegation: delegationOf(req),
		Web: req.Web != nil && *req.Web,
	}

	if req.OutputSchema != nil {
		raw, err := json.Marshal(req.OutputSchema)
		if err != nil {
			return db.CreateAgentParams{}, err
		}
		p.OutputSchema = raw
	}

	if pm := req.ParserModel; pm != nil {
		p.ParserModelName, p.ParserMaxTokens, p.ParserTemperature = &pm.Name, pm.MaxTokens, pm.Temperature
	}

	return p, nil
}

func updateParams(id uuid.UUID, c db.CreateAgentParams) db.UpdateAgentParams {
	return db.UpdateAgentParams{
		ID: id, Name: c.Name, Description: c.Description, Instructions: c.Instructions,
		ModelName: c.ModelName, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
		Pattern: c.Pattern, MaxSteps: c.MaxSteps, SteerContinuations: c.SteerContinuations,
		OutputSchema: c.OutputSchema, Parser: c.Parser,
		ParserModelName: c.ParserModelName, ParserMaxTokens: c.ParserMaxTokens, ParserTemperature: c.ParserTemperature,
		TokenBudget: c.TokenBudget, Delegation: c.Delegation, Web: c.Web,
	}
}

func grant(ctx context.Context, q Queries, row db.Agent, req agentRequest) error {
	for _, g := range req.MCPServers {
		err := q.AddAgentMCPServer(ctx, db.AddAgentMCPServerParams{
			AgentID: row.ID, McpServerID: g.ID, TeamID: row.TeamID, Allow: g.Allow,
		})
		if err != nil {
			return err
		}
	}

	if len(req.Skills) > 0 {
		if err := q.AddAgentSkills(ctx, db.AddAgentSkillsParams{AgentID: row.ID, SkillIds: req.Skills, TeamID: row.TeamID}); err != nil {
			return err
		}
	}
	if len(req.Delegates) == 0 {
		return nil
	}
	return q.AddAgentDelegates(ctx, db.AddAgentDelegatesParams{AgentID: row.ID, DelegateIds: req.Delegates, TeamID: row.TeamID})
}

func requested(req agentRequest) grants {
	g := grants{servers: slices.Clone(req.MCPServers), skills: slices.Clone(req.Skills), delegates: slices.Clone(req.Delegates)}
	slices.SortFunc(g.servers, func(a, b serverGrant) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	slices.SortFunc(g.skills, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	slices.SortFunc(g.delegates, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return g
}

func stored(rows []db.AgentMcpServer, skills, delegates []uuid.UUID) grants {
	servers := make([]serverGrant, len(rows))
	for i, row := range rows {
		servers[i] = serverGrant{ID: row.McpServerID, Allow: row.Allow}
	}
	return grants{servers: servers, skills: skills, delegates: delegates}
}

func page(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListAgentsByTeamParams, bool) {
	params := db.ListAgentsByTeamParams{TeamID: teamID, PageSize: limit + 1}
	q, ok := api.Query(w, r)
	if !ok {
		return params, false
	}
	params.Q = q

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c agentCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
}

func view(row db.Agent, g grants) (detail, error) {
	d := detail{
		summary:            viewSummary(row),
		Instructions:       row.Instructions,
		MaxSteps:           row.MaxSteps,
		TokenBudget:        row.TokenBudget,
		SteerContinuations: row.SteerContinuations,
		Parser:             row.Parser,
		MCPServers:         g.servers,
		Skills:             g.skills,
		Delegation:         row.Delegation,
		Delegates:          g.delegates,
		Web:                row.Web,
	}
	if d.MCPServers == nil {
		d.MCPServers = []serverGrant{}
	}
	if d.Skills == nil {
		d.Skills = []uuid.UUID{}
	}
	if d.Delegates == nil {
		d.Delegates = []uuid.UUID{}
	}

	if row.OutputSchema != nil {
		var o outputSchema
		if err := json.Unmarshal(row.OutputSchema, &o); err != nil {
			return detail{}, err
		}
		d.OutputSchema = &o
	}

	if row.ParserModelName != nil {
		d.ParserModel = &parserModel{Name: *row.ParserModelName, MaxTokens: row.ParserMaxTokens, Temperature: row.ParserTemperature}
	}

	return d, nil
}

func viewSummary(row db.Agent) summary {
	return summary{
		ID: row.ID, TeamID: row.TeamID, Name: row.Name, Description: row.Description,
		Kind:      Kind(row.Kind),
		Model:     modelRef{Name: row.ModelName, MaxTokens: row.MaxTokens, Temperature: row.Temperature},
		Pattern:   row.Pattern,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
