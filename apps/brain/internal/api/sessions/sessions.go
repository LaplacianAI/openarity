package sessions

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Store interface {
	GetChannel(ctx context.Context, id uuid.UUID) (db.Channel, error)
	GetSession(ctx context.Context, id uuid.UUID) (db.Session, error)
	ListSessionsByChannel(ctx context.Context, arg db.ListSessionsByChannelParams) ([]db.Session, error)
	ListSessionsByTeam(ctx context.Context, arg db.ListSessionsByTeamParams) ([]db.Session, error)
	ListMessagesBySession(ctx context.Context, arg db.ListMessagesBySessionParams) ([]db.Message, error)
}

type handler struct {
	logger *slog.Logger
	store  Store
}

func New(logger *slog.Logger, s Store) *api.Router {
	h := &handler{logger: logger, store: s}

	r := api.NewRouter("/teams")
	r.Get("/{id}/channels/{channelID}/sessions", h.listByChannel)
	r.Get("/{id}/sessions", h.listByTeam)
	r.Get("/{id}/sessions/{sessionID}/messages", h.listMessages)
	return r
}

func (h *handler) listByChannel(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	channelID, ok := h.owned(w, r, "channelID", func(id uuid.UUID) (uuid.UUID, error) {
		row, err := h.store.GetChannel(r.Context(), id)
		return row.TeamID, err
	}, teamID)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListSessionsByChannelParams{ChannelID: &channelID, PageSize: limit + 1}
	if c, ok := h.cursor(w, r); !ok {
		return
	} else if c != nil {
		params.UseCursor = true
		params.AfterLastMessageAt = c.LastMessageAt
		params.AfterID = c.ID
	}

	rows, err := h.store.ListSessionsByChannel(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list sessions", err)
		return
	}
	h.writeSessions(w, u, rows, limit)
}

func (h *handler) listByTeam(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListSessionsByTeamParams{TeamID: teamID, PageSize: limit + 1}
	if c, ok := h.cursor(w, r); !ok {
		return
	} else if c != nil {
		params.UseCursor = true
		params.AfterLastMessageAt = c.LastMessageAt
		params.AfterID = c.ID
	}

	rows, err := h.store.ListSessionsByTeam(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list sessions", err)
		return
	}
	h.writeSessions(w, u, rows, limit)
}

func (h *handler) listMessages(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	sessionID, ok := h.owned(w, r, "sessionID", func(id uuid.UUID) (uuid.UUID, error) {
		row, err := h.store.GetSession(r.Context(), id)
		return row.TeamID, err
	}, teamID)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListMessagesBySessionParams{SessionID: sessionID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c messageCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterReceivedAt = c.ReceivedAt
		params.AfterID = c.ID
	}

	rows, err := h.store.ListMessagesBySession(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list messages", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.Message) any { return messageCursor{ReceivedAt: row.ReceivedAt, ID: row.ID} },
		func(row db.Message) message {
			return message{
				ID: row.ID, ExternalID: row.ExternalID, UserID: row.UserID,
				Text: row.Text, SentAt: row.SentAt, ReceivedAt: row.ReceivedAt,
			}
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page messages", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) owned(
	w http.ResponseWriter, r *http.Request, param string,
	teamOf func(uuid.UUID) (uuid.UUID, error), teamID uuid.UUID,
) (uuid.UUID, bool) {
	u := api.Caller(r)

	id, err := uuid.Parse(r.PathValue(param))
	if err != nil {
		http.Error(w, param+" must be a uuid", http.StatusBadRequest)
		return uuid.Nil, false
	}

	owner, err := teamOf(id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read "+param, err)
		return uuid.Nil, false
	}

	if owner != teamID {
		http.Error(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}

	return id, true
}

func (h *handler) cursor(w http.ResponseWriter, r *http.Request) (*sessionCursor, bool) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, true
	}

	var c sessionCursor
	if !api.DecodeCursor(w, raw, &c) {
		return nil, false
	}
	return &c, true
}

func (h *handler) writeSessions(w http.ResponseWriter, u *auth.User, rows []db.Session, limit int32) {
	page, err := api.MapPage(rows, limit,
		func(row db.Session) any {
			return sessionCursor{LastMessageAt: row.LastMessageAt, ID: row.ID}
		},
		func(row db.Session) session {
			out := session{
				ID: row.ID, ChannelID: row.ChannelID, Kind: row.Kind, Status: row.Status,
				StartedAt: row.StartedAt, LastMessageAt: row.LastMessageAt,
			}
			if row.ProviderRef != nil {
				out.ProviderRef = *row.ProviderRef
			}
			return out
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page sessions", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}
