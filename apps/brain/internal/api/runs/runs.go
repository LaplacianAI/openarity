package runs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxInputBytes         = 100 << 10
	readEveryConversation = authz.Action("session:read_all")
)

type StartArgs struct {
	ID        uuid.UUID
	TeamID    uuid.UUID
	AgentID   uuid.UUID
	UserID    uuid.UUID
	SessionID *uuid.UUID
	Input     string
	Hub       bool
}

type Store interface {
	GetAgentByName(ctx context.Context, arg db.GetAgentByNameParams) (db.Agent, error)
	GetSessionVisible(ctx context.Context, arg db.GetSessionVisibleParams) (db.Session, error)
	GetRunVisible(ctx context.Context, arg db.GetRunVisibleParams) (db.Run, error)
	RequestRunCancel(ctx context.Context, id uuid.UUID) (db.Run, error)
	ListRunEvents(ctx context.Context, arg db.ListRunEventsParams) ([]db.RunEvent, error)
	ListRunsBySession(ctx context.Context, arg db.ListRunsBySessionParams) ([]db.Run, error)
	StartRun(ctx context.Context, arg StartArgs) (db.Run, error)
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
}

type Starter interface {
	Enqueue(ctx context.Context, runID uuid.UUID) error
}

type handler struct {
	logger  *slog.Logger
	store   Store
	authz   api.Authorizer
	starter Starter
}

func New(logger *slog.Logger, s Store, a api.Authorizer, starter Starter) *api.Router {
	h := &handler{logger: logger, store: s, authz: a, starter: starter}

	r := api.NewRouter("/teams")
	r.Post("/{id}/runs", h.start)
	r.Get("/{id}/runs/{runID}", h.get)
	r.Get("/{id}/runs/{runID}/events", h.events)
	r.Get("/{id}/sessions/{sessionID}/runs", h.listBySession)
	r.Post("/{id}/runs/{runID}/cancel", h.cancel)
	return r
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req startRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}
	switch {
	case req.Input == "":
		http.Error(w, "input is required", http.StatusBadRequest)
		return
	case len(req.Input) > maxInputBytes:
		http.Error(w, "input must be at most 100 KiB", http.StatusBadRequest)
		return
	case strings.ContainsRune(req.Input, 0):
		http.Error(w, "input must not contain a NUL character", http.StatusBadRequest)
		return
	}

	if req.SessionID != nil {
		_, err := h.store.GetSessionVisible(r.Context(), db.GetSessionVisibleParams{
			ID: *req.SessionID, TeamID: teamID, Moderator: h.moderates(r, u, teamID), Viewer: u.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "no such session in this team", http.StatusNotFound)
			return
		}
		if err != nil {
			api.Fail(w, h.logger, u, "failed to read the session", err)
			return
		}
	}

	args := StartArgs{
		ID: uuid.New(), TeamID: teamID, UserID: u.ID, SessionID: req.SessionID, Input: req.Input,
		Hub: req.Agent == "",
	}
	if args.Hub {
		if !h.hubCanRun(w, r, teamID) {
			return
		}
	} else {
		agent, err := h.store.GetAgentByName(r.Context(), db.GetAgentByNameParams{TeamID: teamID, Name: req.Agent})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "no agent in this team has that name", http.StatusNotFound)
			return
		}
		if err != nil {
			api.Fail(w, h.logger, u, "failed to read the agent", err)
			return
		}
		args.AgentID = agent.ID
	}

	row, err := h.store.StartRun(r.Context(), args)
	if errors.Is(err, pgx.ErrNoRows) {
		if args.Hub {
			http.Error(w, "no such team", http.StatusNotFound)
			return
		}
		http.Error(w, "no agent in this team has that name", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to start the run", err)
		return
	}

	if err := h.starter.Enqueue(r.Context(), row.ID); err != nil {
		h.logger.WarnContext(r.Context(), "Enqueuing a run failed; the sweep will start it",
			"run_id", row.ID, "error", err)
	}
	api.WriteJSON(w, h.logger, http.StatusAccepted, view(row))
}

func (h *handler) hubCanRun(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) bool {
	s, err := h.store.GetTeamSettings(r.Context(), teamID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		api.Fail(w, h.logger, api.Caller(r), "failed to read the team's settings", err)
		return false
	}
	if s.HubAgentID == nil && s.ModelName == nil {
		http.Error(w, "the team has no model set, so its orchestrator cannot run; set one in the team's settings",
			http.StatusConflict)
		return false
	}
	return true
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	row, _, ok := h.visible(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, view(row))
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	row, _, ok := h.visible(w, r)
	if !ok {
		return
	}

	cancelled, err := h.store.RequestRunCancel(r.Context(), row.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "the run has already finished", http.StatusConflict)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to cancel the run", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusAccepted, view(cancelled))
}

func (h *handler) listBySession(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	sessionID, err := uuid.Parse(r.PathValue("sessionID"))
	if err != nil {
		http.Error(w, "sessionID must be a uuid", http.StatusBadRequest)
		return
	}

	_, err = h.store.GetSessionVisible(r.Context(), db.GetSessionVisibleParams{
		ID: sessionID, TeamID: teamID, Moderator: h.moderates(r, u, teamID), Viewer: u.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the session", err)
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListRunsBySessionParams{SessionID: sessionID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c runCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterCreatedAt = c.CreatedAt
		params.AfterID = c.ID
	}

	rows, err := h.store.ListRunsBySession(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list runs", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Run) any { return runCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		view,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page runs", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) visible(w http.ResponseWriter, r *http.Request) (db.Run, db.GetRunVisibleParams, bool) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return db.Run{}, db.GetRunVisibleParams{}, false
	}

	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		http.Error(w, "runID must be a uuid", http.StatusBadRequest)
		return db.Run{}, db.GetRunVisibleParams{}, false
	}

	params := db.GetRunVisibleParams{ID: id, TeamID: teamID, Moderator: h.moderates(r, u, teamID), Viewer: u.ID}
	row, err := h.store.GetRunVisible(r.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Run{}, db.GetRunVisibleParams{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the run", err)
		return db.Run{}, db.GetRunVisibleParams{}, false
	}
	return row, params, true
}

func (h *handler) moderates(r *http.Request, u *auth.User, teamID uuid.UUID) bool {
	ok, err := h.authz.Can(r.Context(), u, readEveryConversation, authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(readEveryConversation),
			"error", err, "team_id", teamID)
		return false
	}
	return ok
}

func view(r db.Run) run {
	return run{
		ID: r.ID, SessionID: r.SessionID, Agent: r.AgentName, Status: r.Status,
		Input: r.Input, Output: r.Output, Error: r.Error,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		ParentRunID: r.ParentRunID, Depth: r.Depth, Hub: r.Hub,
	}
}
