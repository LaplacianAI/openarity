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
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type Store interface {
	GetChannel(ctx context.Context, id uuid.UUID) (db.Channel, error)
	GetSession(ctx context.Context, id uuid.UUID) (db.Session, error)
	GetSessionFirstAsked(ctx context.Context, sessionID uuid.UUID) (string, error)
	ListSessionsByChannel(ctx context.Context, arg db.ListSessionsByChannelParams) ([]db.ListSessionsByChannelRow, error)
	ListSessionsByTeam(ctx context.Context, arg db.ListSessionsByTeamParams) ([]db.ListSessionsByTeamRow, error)
	ListMessagesBySession(ctx context.Context, arg db.ListMessagesBySessionParams) ([]db.Message, error)
	ListAttachmentsBySession(ctx context.Context, arg db.ListAttachmentsBySessionParams) ([]db.Attachment, error)
	GetAttachmentInSession(ctx context.Context, arg db.GetAttachmentInSessionParams) (db.Attachment, error)
}

const readEveryConversation = authz.Action("session:read_all")

type listed struct {
	Session            db.Session
	ChannelName        *string
	ParticipantEmail   *string
	ParticipantSubject *string
	LastMessage        *string
	LatestRunStatus    *string
	FirstAsked         *string
}

const previewRunes = 140

type handler struct {
	logger  *slog.Logger
	store   Store
	authz   api.Authorizer
	objects Objects
}

func New(logger *slog.Logger, s Store, a api.Authorizer, o Objects) *api.Router {
	h := &handler{logger: logger, store: s, authz: a, objects: o}

	r := api.NewRouter("/teams")
	r.Get("/{id}/channels/{channelID}/sessions", h.listByChannel)
	r.Get("/{id}/sessions", h.listByTeam)
	r.Get("/{id}/sessions/{sessionID}", h.get)
	r.Get("/{id}/sessions/{sessionID}/messages", h.listMessages)

	r.Get("/{id}/sessions/{sessionID}/attachments", h.listAttachments)
	r.Get("/{id}/sessions/{sessionID}/attachments/{attachmentID}", h.getAttachment)
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

	params := db.ListSessionsByChannelParams{
		ChannelID: &channelID,
		PageSize:  limit + 1,
		Moderator: h.moderates(r, u, teamID),
		Viewer:    u.ID,
	}
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

	out := make([]listed, len(rows))
	for i, row := range rows {
		out[i] = listed(row)
	}
	h.writeSessions(w, u, out, limit)
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

	statuses, ok := api.OneOf(w, r, "status", api.SessionStatuses)
	if !ok {
		return
	}
	runStatuses, ok := api.OneOf(w, r, "run_status", api.RunStatuses)
	if !ok {
		return
	}

	params := db.ListSessionsByTeamParams{
		TeamID:      teamID,
		PageSize:    limit + 1,
		Moderator:   h.moderates(r, u, teamID),
		Viewer:      u.ID,
		Statuses:    statuses,
		RunStatuses: runStatuses,
	}

	if raw := r.URL.Query().Get("channel_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "channel_id must be a uuid", http.StatusBadRequest)
			return
		}
		params.ChannelID = &id
	}

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

	out := make([]listed, len(rows))
	for i, row := range rows {
		out[i] = listed(row)
	}
	h.writeSessions(w, u, out, limit)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	row, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}

	out := sessionView(row)
	asked, err := h.store.GetSessionFirstAsked(r.Context(), row.ID)
	switch {
	case err == nil:
		out.FirstAsked = preview(&asked)
	case !errors.Is(err, pgx.ErrNoRows):
		api.Fail(w, h.logger, api.Caller(r), "failed to read what the session was first asked", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

func (h *handler) listMessages(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	session, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListMessagesBySessionParams{SessionID: session.ID, PageSize: limit + 1}
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

func (h *handler) writeSessions(w http.ResponseWriter, u *auth.User, rows []listed, limit int32) {
	page, err := api.MapPage(rows, limit,
		func(row listed) any {
			return sessionCursor{LastMessageAt: row.Session.LastMessageAt, ID: row.Session.ID}
		},
		listedView,
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page sessions", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func sessionView(row db.Session) session {
	out := session{
		ID: row.ID, ChannelID: row.ChannelID, Kind: row.Kind, Status: row.Status,
		StartedAt: row.StartedAt, LastMessageAt: row.LastMessageAt,
	}
	if row.ProviderRef != nil {
		out.ProviderRef = *row.ProviderRef
	}
	return out
}

func listedView(row listed) listedSession {
	participant := row.ParticipantEmail
	if participant == nil {
		participant = row.ParticipantSubject
	}

	s := sessionView(row.Session)
	s.FirstAsked = preview(row.FirstAsked)
	return listedSession{
		session:         s,
		ChannelName:     row.ChannelName,
		Participant:     participant,
		LastMessage:     preview(row.LastMessage),
		LatestRunStatus: row.LatestRunStatus,
	}
}

func preview(text *string) *string {
	if text == nil {
		return nil
	}

	runes := []rune(*text)
	if len(runes) <= previewRunes {
		return text
	}

	cut := string(runes[:previewRunes]) + "…"
	return &cut
}

func (h *handler) visible(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (db.Session, bool) {
	u := api.Caller(r)

	id, err := uuid.Parse(r.PathValue("sessionID"))
	if err != nil {
		http.Error(w, "sessionID must be a uuid", http.StatusBadRequest)
		return db.Session{}, false
	}

	row, err := h.store.GetSession(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Session{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read sessionID", err)
		return db.Session{}, false
	}

	if row.TeamID != teamID {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Session{}, false
	}

	if row.Kind == "direct" &&
		(row.UserID == nil || *row.UserID != u.ID) &&
		!h.moderates(r, u, teamID) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Session{}, false
	}

	return row, true
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
