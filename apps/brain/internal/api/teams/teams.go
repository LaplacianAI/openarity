package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	maxNameBytes        = 200
	codeUniqueViolation = "23505"
	maxSubjectMatches   = 10
)

type Store interface {
	CreateTeam(ctx context.Context, name string) (db.Team, error)
	GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	ListTeams(ctx context.Context, arg db.ListTeamsParams) ([]db.Team, error)
	ListUserTeams(ctx context.Context, userID uuid.UUID) ([]db.ListUserTeamsRow, error)
	ListTeamMembers(ctx context.Context, arg db.ListTeamMembersParams) ([]db.ListTeamMembersRow, error)
	AddTeamMember(ctx context.Context, arg db.AddTeamMemberParams) (db.TeamMember, error)
	RemoveTeamMember(ctx context.Context, arg db.RemoveTeamMemberParams) error
	FindUsersBySubject(ctx context.Context, arg db.FindUsersBySubjectParams) ([]db.FindUsersBySubjectRow, error)
	ActionsFor(ctx context.Context, role string) ([]string, error)
	GetLiveTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	SoftDeleteTeam(ctx context.Context, id uuid.UUID) (int64, error)
	RestoreTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
	ListDeletedTeams(ctx context.Context, arg db.ListDeletedTeamsParams) ([]db.Team, error)
}

type SuperAdmins interface {
	IsSuperAdmin(u *auth.User) bool
}

type handler struct {
	logger      *slog.Logger
	store       Store
	superAdmins SuperAdmins
	purgeAfter  time.Duration
}

func New(logger *slog.Logger, s Store, a SuperAdmins, purgeAfter time.Duration) *api.Router {
	h := &handler{logger: logger, store: s, superAdmins: a, purgeAfter: purgeAfter}

	r := api.NewRouter("/teams")
	r.Post("", h.create)
	r.Get("", h.list)
	r.Get("/{id}", h.get)
	r.Delete("/{id}", h.deleteTeam)
	r.Post("/{id}/restore", h.restoreTeam)
	r.Get("/{id}/permissions", h.permissions)
	r.Get("/{id}/members", h.listMembers)
	r.Post("/{id}/members", h.addMember)
	r.Delete("/{id}/members/{userID}", h.removeMember)
	return r
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	var req createRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	name, ok := api.Name(req.Name, maxNameBytes)
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
		api.Fail(w, h.logger, u, "failed to create team", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, team{ID: row.ID, Name: row.Name})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	if !h.superAdmins.IsSuperAdmin(u) {
		teams, err := h.myTeams(r.Context(), u)
		if err != nil {
			api.Fail(w, h.logger, u, "failed to list teams", err)
			return
		}
		api.WriteJSON(w, h.logger, http.StatusOK, api.Page[team]{Items: teams})
		return
	}

	deleted, ok := deletedParam(w, r)
	if !ok {
		return
	}

	params, ok := h.teamPage(w, r, limit)
	if !ok {
		return
	}

	var rows []db.Team
	var err error
	if deleted {
		rows, err = h.store.ListDeletedTeams(r.Context(), db.ListDeletedTeamsParams(params))
	} else {
		rows, err = h.store.ListTeams(r.Context(), params)
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list teams", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Team) any { return teamCursor{CreatedAt: row.CreatedAt, ID: row.ID} },
		func(row db.Team) team { return h.wire(u, row) },
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page teams", err)
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

func deletedParam(w http.ResponseWriter, r *http.Request) (bool, bool) {
	raw := r.URL.Query().Get("deleted")
	if raw == "" {
		return false, true
	}

	deleted, err := strconv.ParseBool(raw)
	if err != nil {
		http.Error(w, "deleted must be true or false", http.StatusBadRequest)
		return false, false
	}
	return deleted, true
}

func (h *handler) wire(u *auth.User, row db.Team) team {
	t := team{ID: row.ID, Name: row.Name}
	if role, ok := u.RoleIn(row.ID); ok {
		t.Role = &role
	}
	if row.DeletedAt != nil {
		purgeAt := row.DeletedAt.Add(h.purgeAfter)
		t.DeletedAt, t.PurgeAt = row.DeletedAt, &purgeAt
	}
	return t
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
	u := api.Caller(r)

	id, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.visibleTeam(w, r, u, id)
	if !ok {
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, h.wire(u, row))
}

func (h *handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	id, ok := pathTeam(w, r)
	if !ok {
		return
	}

	n, err := h.store.SoftDeleteTeam(r.Context(), id)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to delete team", err)
		return
	}
	if n == 1 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	row, err := h.store.GetTeam(r.Context(), id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		api.Fail(w, h.logger, u, "failed to read team", err)
	case row.DeletedAt != nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "the team has runs in progress", http.StatusConflict)
	}
}

func (h *handler) restoreTeam(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	id, ok := pathTeam(w, r)
	if !ok {
		return
	}

	row, err := h.store.GetTeam(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team", err)
		return
	}

	if row.DeletedAt != nil {
		row, err = h.store.RestoreTeam(r.Context(), id)
		if err != nil {
			h.failRestore(w, u, err)
			return
		}
	}

	api.WriteJSON(w, h.logger, http.StatusOK, h.wire(u, row))
}

func (h *handler) failRestore(w http.ResponseWriter, u *auth.User, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation:
		http.Error(w, "a team with that name already exists", http.StatusConflict)
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		api.Fail(w, h.logger, u, "failed to restore team", err)
	}
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	id, ok := api.RequireTeam(w, r, h.logger)
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
		api.Fail(w, h.logger, u, "failed to list members", err)
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
		api.Fail(w, h.logger, u, "failed to page members", err)
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
	u := api.Caller(r)

	id, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	var req addMemberRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	userID, ok := h.namedUser(w, u, r, req)
	if !ok {
		return
	}

	_, err := h.store.AddTeamMember(r.Context(), db.AddTeamMemberParams{
		TeamID: id,
		UserID: userID,
		Role:   req.Role,
	})
	if err != nil {
		h.failMembership(w, u, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) namedUser(
	w http.ResponseWriter, u *auth.User, r *http.Request, req addMemberRequest,
) (uuid.UUID, bool) {
	subject := ""
	if req.Subject != nil {
		subject = strings.TrimSpace(*req.Subject)
	}

	switch {
	case req.UserID != nil && subject != "":
		http.Error(w, "give user_id or subject, not both", http.StatusBadRequest)
		return uuid.Nil, false
	case req.UserID != nil:
		return *req.UserID, true
	case subject == "":
		http.Error(w, "give a user_id or a subject", http.StatusBadRequest)
		return uuid.Nil, false
	}

	rows, err := h.store.FindUsersBySubject(r.Context(), db.FindUsersBySubjectParams{
		Subject: subject, PageSize: maxSubjectMatches,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to look up a subject", err)
		return uuid.Nil, false
	}

	switch len(rows) {
	case 0:
		http.Error(w, "no user has that subject", http.StatusNotFound)
		return uuid.Nil, false
	case 1:
		return rows[0].ID, true
	default:
		http.Error(w, ambiguousSubject(rows), http.StatusConflict)
		return uuid.Nil, false
	}
}

func ambiguousSubject(rows []db.FindUsersBySubjectRow) string {
	named := make([]string, len(rows))
	for i, row := range rows {
		named[i] = fmt.Sprintf("%s (%s)", row.ID, row.Issuer)
	}
	return fmt.Sprintf("%d users have that subject — retry with user_id: %s",
		len(rows), strings.Join(named, ", "))
}

func pathTeam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "team id must be a uuid", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *handler) removeMember(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	id, ok := api.RequireTeam(w, r, h.logger)
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
		api.Fail(w, h.logger, u, "failed to remove member", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) visibleTeam(w http.ResponseWriter, r *http.Request, u *auth.User, id uuid.UUID) (db.Team, bool) {
	read := h.store.GetLiveTeam
	if h.superAdmins.IsSuperAdmin(u) {
		read = h.store.GetTeam
	}

	row, err := read(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Team{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read team", err)
		return db.Team{}, false
	}

	return row, true
}

func (h *handler) failMembership(w http.ResponseWriter, u *auth.User, err error) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		api.Fail(w, h.logger, u, "failed to add member", err)
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
		api.Fail(w, h.logger, u, "failed to add member", err)
	}
}

func (h *handler) permissions(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	actions := []string{}
	if role, member := u.RoleIn(teamID); member {
		granted, err := h.store.ActionsFor(r.Context(), role)
		if err != nil {
			api.Fail(w, h.logger, u, "failed to read the role's permissions", err)
			return
		}
		actions = append(actions, granted...)
		slices.Sort(actions)
	}

	api.WriteJSON(w, h.logger, http.StatusOK, permissions{
		Actions:    actions,
		SuperAdmin: h.superAdmins.IsSuperAdmin(u),
	})
}
