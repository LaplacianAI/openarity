package users

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Store interface {
	ListUsers(ctx context.Context, arg db.ListUsersParams) ([]db.ListUsersRow, error)
}

type Authorizer interface {
	CanInAnyTeam(ctx context.Context, u *auth.User, action authz.Action) (bool, error)
}

type handler struct {
	logger *slog.Logger
	store  Store
	authz  Authorizer
}

func New(logger *slog.Logger, s Store, a Authorizer) *api.Router {
	h := &handler{logger: logger, store: s, authz: a}

	r := api.NewRouter("/users")
	r.Get("", h.list)
	return r
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		h.logger.Error("users ran without a user — check the middleware order")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	allowed, err := h.authz.CanInAnyTeam(r.Context(), u, authz.ActionMembershipWrite)
	if err != nil {
		h.fail(w, u, "authorisation check failed", err)
		return
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params, ok := h.userPage(w, r, limit)
	if !ok {
		return
	}

	rows, err := h.store.ListUsers(r.Context(), params)
	if err != nil {
		h.fail(w, u, "failed to list users", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.ListUsersRow) any { return userCursor{Subject: row.Subject, ID: row.ID} },
		func(row db.ListUsersRow) user {
			return user{ID: row.ID, Subject: row.Subject, Email: row.Email}
		},
	)
	if err != nil {
		h.fail(w, u, "failed to page users", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) userPage(w http.ResponseWriter, r *http.Request, limit int32) (db.ListUsersParams, bool) {
	params := db.ListUsersParams{PageSize: limit + 1}

	if subject := strings.TrimSpace(r.URL.Query().Get("subject")); subject != "" {
		params.UseSubject = true
		params.Subject = subject
	}

	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return params, true
	}

	var c userCursor
	if !api.DecodeCursor(w, raw, &c) {
		return params, false
	}

	params.UseCursor = true
	params.AfterSubject = c.Subject
	params.AfterID = c.ID
	return params, true
}

func (h *handler) fail(w http.ResponseWriter, u *auth.User, msg string, err error) {
	h.logger.Error(msg, "subject", u.Subject, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
