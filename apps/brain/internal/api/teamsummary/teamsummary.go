package teamsummary

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	readEveryConversation = authz.Action("session:read_all")
	readTheQueue          = authz.Action("channel:read")
)

type Store interface {
	TeamSummary(ctx context.Context, arg db.TeamSummaryParams) (db.TeamSummaryRow, error)
}

type handler struct {
	logger *slog.Logger
	store  Store
	authz  api.Authorizer
}

type runCounts struct {
	Queued    int64 `json:"queued"`
	Running   int64 `json:"running"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Cancelled int64 `json:"cancelled"`
}

type summary struct {
	Runs24h        runCounts `json:"runs_24h"`
	Sessions       int64     `json:"sessions"`
	WaitingSenders *int64    `json:"waiting_senders"`
	ServersFailing int64     `json:"servers_failing"`
	Agents         int64     `json:"agents"`
}

func New(logger *slog.Logger, s Store, a api.Authorizer) *api.Router {
	h := &handler{logger: logger, store: s, authz: a}

	r := api.NewRouter("/teams")
	r.Get("/{id}/summary", h.get)
	return r
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, err := h.store.TeamSummary(r.Context(), db.TeamSummaryParams{
		TeamID:    teamID,
		Moderator: h.can(r, u, teamID, readEveryConversation),
		Viewer:    u.ID,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to summarise the team", err)
		return
	}

	out := summary{
		Runs24h: runCounts{
			Queued: row.RunsQueued, Running: row.RunsRunning, Succeeded: row.RunsSucceeded,
			Failed: row.RunsFailed, Cancelled: row.RunsCancelled,
		},
		Sessions:       row.Sessions,
		Agents:         row.Agents,
		ServersFailing: row.ServersFailing,
	}
	if h.can(r, u, teamID, readTheQueue) {
		out.WaitingSenders = &row.WaitingSenders
	}

	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) can(r *http.Request, u *auth.User, teamID uuid.UUID, a authz.Action) bool {
	ok, err := h.authz.Can(r.Context(), u, a, authz.Resource{TeamID: teamID})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "failed to check "+string(a), "error", err, "team_id", teamID)
		return false
	}
	return ok
}
