package teams

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxNameBytes        = 200
	codeUniqueViolation = "23505"
)

type Store interface {
	CreateTeam(ctx context.Context, name string) (db.Team, error)
	GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	ListTeams(ctx context.Context, arg db.ListTeamsParams) ([]db.Team, error)
	ListUserTeams(ctx context.Context, userID uuid.UUID) ([]db.ListUserTeamsRow, error)
	ListTeamMembers(ctx context.Context, arg db.ListTeamMembersParams) ([]db.ListTeamMembersRow, error)
	AddTeamMember(ctx context.Context, arg db.AddTeamMemberParams) (db.TeamMember, error)
	RemoveTeamMember(ctx context.Context, arg db.RemoveTeamMemberParams) error
}

type Authorizer interface {
	IsSuperAdmin(u *auth.User) bool
	Can(ctx context.Context, u *auth.User, action authz.Action, r authz.Resource) (bool, error)
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
	r.Get("/{id}/members", h.listMembers)
	r.Post("/{id}/members", h.addMember)
	r.Delete("/{id}/members/{userID}", h.removeMember)
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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation {
			http.Error(w, "a team with that name already exists", http.StatusConflict)
			return
		}
		h.fail(w, u, "failed to create team", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, team{ID: row.ID, Name: row.Name})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u, ok := h.caller(w, r)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	if !h.authz.IsSuperAdmin(u) {
		teams, err := h.myTeams(r.Context(), u)
		if err != nil {
			h.fail(w, u, "failed to list teams", err)
			return
		}
		api.WriteJSON(w, h.logger, http.StatusOK, api.Page[team]{Items: teams})
		return
	}

	params, ok := h.teamPage(w, r, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListTeams(r.Context(), params)
	if err != nil {
		h.fail(w, u, "failed to list teams", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Team) any { return teamCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		func(row db.Team) team { return team{ID: row.ID, Name: row.Name} },
	)
	if err != nil {
		h.fail(w, u, "failed to page teams", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) teamPage(w http.ResponseWriter, r *http.Request, limit int32) (db.ListTeamsParams, bool) {
	params := db.ListTeamsParams{PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c teamCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterCreatedAt = c.CreatedAt
	params.AfterID = c.ID
	return params, true
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

	id, ok := h.teamID(w, r)
	if !ok {
		return
	}

	row, ok := h.visibleTeam(w, r, u, id)
	if !ok {
		return
	}

	out := team{ID: row.ID, Name: row.Name}
	if role, isMember := u.RoleIn(id); isMember {
		out.Role = &role
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request) {
	u, ok := h.caller(w, r)
	if !ok {
		return
	}

	id, ok := h.teamID(w, r)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	if _, ok := h.visibleTeam(w, r, u, id); !ok {
		return
	}

	params, ok := h.memberPage(w, r, id, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListTeamMembers(r.Context(), params)
	if err != nil {
		h.fail(w, u, "failed to list members", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.ListTeamMembersRow) any {
			return memberCursor{Subject: row.Subject, ID: row.ID}
		},
		func(row db.ListTeamMembersRow) member {
			return member{UserID: row.ID, Subject: row.Subject, Email: row.Email, Role: row.Role}
		},
	)
	if err != nil {
		h.fail(w, u, "failed to page members", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) memberPage(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, limit int32) (db.ListTeamMembersParams, bool) {
	params := db.ListTeamMembersParams{TeamID: teamID, PageSize: limit + 1}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c memberCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterSubject = c.Subject
	params.AfterID = c.ID
	return params, true
}

func (h *handler) addMember(w http.ResponseWriter, r *http.Request) {
	u, id, ok := h.mayWriteMembers(w, r)
	if !ok {
		return
	}

	var req addMemberRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	_, err := h.store.AddTeamMember(r.Context(), db.AddTeamMemberParams{
		TeamID: id, UserID: req.UserID, Role: req.Role,
	})
	if err != nil {
		h.failMembership(w, u, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeMember(w http.ResponseWriter, r *http.Request) {
	u, id, ok := h.mayWriteMembers(w, r)
	if !ok {
		return
	}

	userID, err := uuid.Parse(r.PathValue("userID"))
	if err != nil {
		http.Error(w, "user id must be a uuid", http.StatusBadRequest)
		return
	}

	if err := h.store.RemoveTeamMember(r.Context(), db.RemoveTeamMemberParams{
		TeamID: id, UserID: userID,
	}); err != nil {
		h.fail(w, u, "failed to remove member", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) mayWriteMembers(w http.ResponseWriter, r *http.Request) (*auth.User, uuid.UUID, bool) {
	u, ok := h.caller(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}

	id, ok := h.teamID(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}

	allowed, err := h.authz.Can(r.Context(), u, authz.ActionMemberWrite, authz.Resource{TeamID: id})
	if err != nil {
		h.fail(w, u, "authorisation check failed", err)
		return nil, uuid.Nil, false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, uuid.Nil, false
	}

	return u, id, true
}

func (h *handler) visibleTeam(w http.ResponseWriter, r *http.Request, u *auth.User, id uuid.UUID) (db.Team, bool) {
	if _, isMember := u.RoleIn(id); !isMember && !h.authz.IsSuperAdmin(u) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Team{}, false
	}

	row, err := h.store.GetTeam(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Team{}, false
	}
	if err != nil {
		h.fail(w, u, "failed to read team", err)
		return db.Team{}, false
	}

	return row, true
}

func (h *handler) teamID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "id must be a uuid", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *handler) fail(w http.ResponseWriter, u *auth.User, msg string, err error) {
	h.logger.Error(msg, "subject", u.Subject, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *handler) failMembership(w http.ResponseWriter, u *auth.User, err error) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		h.fail(w, u, "failed to add member", err)
		return
	}

	switch {
	case pgErr.Code == codeUniqueViolation:
		http.Error(w, "already a member", http.StatusConflict)
	case pgErr.ConstraintName == "team_members_team_id_fkey":
		http.Error(w, "not found", http.StatusNotFound)
	case pgErr.ConstraintName == "team_members_user_id_fkey":
		http.Error(w, "unknown user", http.StatusBadRequest)
	case pgErr.ConstraintName == "team_members_role_fkey":
		http.Error(w, "unknown role", http.StatusBadRequest)
	default:
		h.fail(w, u, "failed to add member", err)
	}
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
