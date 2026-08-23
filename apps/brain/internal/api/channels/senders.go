package channels

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (h *handler) channelInTeam(w http.ResponseWriter, r *http.Request) (db.Channel, bool) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return db.Channel{}, false
	}

	channelID, err := uuid.Parse(r.PathValue("channelID"))
	if err != nil {
		http.Error(w, "channel id must be a uuid", http.StatusBadRequest)
		return db.Channel{}, false
	}

	row, err := h.store.GetChannel(r.Context(), channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Channel{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read channel", err)
		return db.Channel{}, false
	}

	if row.TeamID != teamID {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Channel{}, false
	}

	return row, true
}

func (h *handler) listPending(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	channel, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListPendingSendersParams{ChannelID: channel.ID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c pendingCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterFirstSeen = c.FirstSeen
		params.AfterRef = c.Ref
	}

	rows, err := h.store.ListPendingSenders(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list pending senders", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.PendingSender) any {
			return pendingCursor{FirstSeen: row.FirstSeen, Ref: row.SenderRef}
		},
		func(row db.PendingSender) pendingSender {
			return pendingSender{
				SenderRef:  row.SenderRef,
				SenderName: row.SenderName,
				SeenCount:  row.SeenCount,
				FirstSeen:  row.FirstSeen,
				LastSeen:   row.LastSeen,
			}
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page pending senders", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) listSenders(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	channel, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListChannelSendersParams{ChannelID: channel.ID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c senderCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterCreatedAt = c.CreatedAt
		params.AfterRef = c.Ref
	}

	rows, err := h.store.ListChannelSenders(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list senders", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.ChannelSender) any {
			return senderCursor{CreatedAt: row.CreatedAt, Ref: row.SenderRef}
		},
		func(row db.ChannelSender) channelSender {
			return channelSender{
				SenderRef: row.SenderRef,
				UserID:    row.UserID,
				CreatedAt: row.CreatedAt,
			}
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page senders", err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusOK, page)
}

func (h *handler) approve(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	channel, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}

	var req approveRequest
	if !api.DecodeJSON(w, r, &req) {
		return
	}

	ref, ok := senderRef(w, req.SenderRef)
	if !ok {
		return
	}

	if req.UserID == uuid.Nil {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	_, err := h.store.FindTeamMember(r.Context(), db.FindTeamMemberParams{
		TeamID: channel.TeamID, UserID: req.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "that user is not in this team", http.StatusBadRequest)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to check team membership", err)
		return
	}

	if err := h.store.ApproveSender(r.Context(), db.ApproveSenderParams{
		ChannelID: channel.ID, SenderRef: ref, UserID: req.UserID,
	}); err != nil {
		api.Fail(w, h.logger, u, "failed to approve sender", err)
		return
	}

	h.logger.Info("channel sender approved",
		"subject", u.Subject, "channel_id", channel.ID, "sender_ref", ref, "user_id", req.UserID)

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeSender(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	channel, ok := h.channelInTeam(w, r)
	if !ok {
		return
	}

	ref, ok := senderRef(w, r.URL.Query().Get("ref"))
	if !ok {
		return
	}

	if err := h.store.RemoveSender(r.Context(), db.RemoveSenderParams{
		ChannelID: channel.ID, SenderRef: ref,
	}); err != nil {
		api.Fail(w, h.logger, u, "failed to remove sender", err)
		return
	}

	h.logger.Info("channel sender removed",
		"subject", u.Subject, "channel_id", channel.ID, "sender_ref", ref)

	w.WriteHeader(http.StatusNoContent)
}

func senderRef(w http.ResponseWriter, raw string) (string, bool) {
	ref := strings.TrimSpace(raw)
	switch {
	case ref == "":
		http.Error(w, "sender_ref is required", http.StatusBadRequest)
		return "", false
	case utf8.RuneCountInString(ref) > gateway.SenderRefMax:
		http.Error(w, "sender_ref is too long", http.StatusBadRequest)
		return "", false
	}
	return ref, true
}

var _ = url.QueryEscape
