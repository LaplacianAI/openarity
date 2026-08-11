package teams

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
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const maxNameBytes = 200

type Store interface {
	CreateTeam(ctx context.Context, name string) (db.Team, error)
	GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	ListTeams(ctx context.Context) ([]db.Team, error)
	ListUserTeams(ctx context.Context, userID uuid.UUID) ([]db.ListUserTeamsRow, error)
}

type Authorizer interface {
	IsSuperAdmin(u *auth.User) bool
}

type handler struct {
	logger *slog.Logger
	store  Store
	authz  Authorizer
}

func New(logger *slog.Logger, s Store, a Authorizer) *api.Router {
	h := &handler{logger: logger, store: s, authz: a}

	r := api.NewRouter("/teams")
	r.Post("", h.create)
	r.Get("", h.list)
	r.Get("/{id}", h.get)
	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u, ok := h.caller(w, r)
	if !ok {
		return
	}

	if !h.authz.IsSuperAdmin(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req createRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	name, ok := teamName(req.Name)
	if !ok {
		http.Error(w, "name must be between 1 and 200 characters", http.StatusBadRequest)
		return
	}

	row, err := h.store.CreateTeam(r.Context(), name)
	if err != nil {
		h.logger.Error("failed to create team", "subject", u.Subject, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, team{ID: row.ID, Name: row.Name})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u, ok := h.caller(w, r)
	if !ok {
		return
	}

	var (
		teams []team
		err   error
	)
	if h.authz.IsSuperAdmin(u) {
		teams, err = h.allTeams(r.Context())
	} else {
		teams, err = h.myTeams(r.Context(), u)
	}
	if err != nil {
		h.logger.Error("failed to list teams", "subject", u.Subject, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, teams)
}

func (h *handler) allTeams(ctx context.Context) ([]team, error) {
	rows, err := h.store.ListTeams(ctx)
	if err != nil {
		return nil, err
	}

	teams := make([]team, len(rows))
	for i, row := range rows {
		teams[i] = team{ID: row.ID, Name: row.Name}
	}
	return teams, nil
}

func (h *handler) myTeams(ctx context.Context, u *auth.User) ([]team, error) {
	rows, err := h.store.ListUserTeams(ctx, u.ID)
	if err != nil {
		return nil, err
	}

	teams := make([]team, len(rows))
	for i, row := range rows {
		role := row.Role
		teams[i] = team{ID: row.ID, Name: row.Name, Role: &role}
	}
	return teams, nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u, ok := h.caller(w, r)
	if !ok {
		return
	}

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "id must be a uuid", http.StatusBadRequest)
		return
	}

	role, member := u.RoleIn(id)
	if !member && !h.authz.IsSuperAdmin(u) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	row, err := h.store.GetTeam(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to read team", "subject", u.Subject, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	out := team{ID: row.ID, Name: row.Name}
	if member {
		out.Role = &role
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) caller(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		h.logger.Error("teams ran without a user — check the middleware order")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, false
	}
	return u, true
}

func teamName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxNameBytes {
		return "", false
	}
	return name, true
}
