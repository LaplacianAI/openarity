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
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
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

func (h *handler) listTeamPending(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)

	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}

	limit, ok := api.Limit(w, r)
	if !ok {
		return
	}

	params := db.ListTeamPendingSendersParams{TeamID: teamID, PageSize: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c teamPendingCursor
		if !api.DecodeCursor(w, raw, &c) {
			return
		}
		params.UseCursor = true
		params.AfterFirstSeen = c.FirstSeen
		params.AfterChannelID = c.Channel
		params.AfterRef = c.Ref
	}

	rows, err := h.store.ListTeamPendingSenders(r.Context(), params)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to list the team's pending senders", err)
		return
	}

	page, err := api.MapPage(rows, limit,
		func(row db.ListTeamPendingSendersRow) any {
			p := row.PendingSender
			return teamPendingCursor{FirstSeen: p.FirstSeen, Channel: p.ChannelID, Ref: p.SenderRef}
		},
		func(row db.ListTeamPendingSendersRow) teamPendingSender {
			p := row.PendingSender
			return teamPendingSender{
				pendingSender: pendingSender{
					SenderRef:  p.SenderRef,
					SenderName: p.SenderName,
					SeenCount:  p.SeenCount,
					FirstSeen:  p.FirstSeen,
					LastSeen:   p.LastSeen,
				},
				ChannelID:       p.ChannelID,
				ChannelName:     row.ChannelName,
				ChannelProvider: row.ChannelProvider,
			}
		},
	)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to page the team's pending senders", err)
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

	role, err := h.store.FindTeamMember(r.Context(), db.FindTeamMemberParams{
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
	if !h.covers(w, r, u, channel.TeamID, role) {
		return
	}
	// Approving a ref already bound to somebody else takes it from them, so
	// the caller must cover whoever holds it now as well.
	holder, ok := h.coversHolder(w, r, u, channel, ref, req.UserID)
	if !ok {
		return
	}

	// The write names the holder that was checked, so a binding moved since
	// is refused rather than taken from somebody nobody checked.
	approved, err := h.store.ApproveSender(r.Context(), db.ApproveSenderParams{
		ChannelID: channel.ID, SenderRef: ref, UserID: req.UserID, ExpectedHolder: holder,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to approve sender", err)
		return
	}
	if approved.Linked == 0 {
		http.Error(w, errMoved, http.StatusConflict)
		return
	}
	if approved.Admitted != 0 && h.kick != nil {
		if err := h.kick.KickNotice(r.Context(), approved.Admitted); err != nil {
			h.logger.Warn("failed to kick the admitted notice; the sweep sends it",
				"notice", approved.Admitted, "error", err)
		}
	}

	h.logger.Info("channel sender approved",
		"subject", u.Subject, "channel_id", channel.ID, "sender_ref", ref, "user_id", req.UserID)

	w.WriteHeader(http.StatusNoContent)
}

// covers answers whether the caller holds every permission role grants in the
// team, and writes the refusal when they do not. sender:approve is apart from
// membership:write, so without this an approver could make a stranger speak
// as an admin. Asking Can, rather than comparing two roles, keeps a super
// admin and an operator's own roles right.
//
// A failed read is a 500, never a refusal: answering "you may not" for a
// database blip sends somebody to audit roles that were never wrong.
func (h *handler) covers(w http.ResponseWriter, r *http.Request, u *auth.User, team uuid.UUID, role string) bool {
	perms, err := h.store.ListRolePermissions(r.Context(), role)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the user's role", err)
		return false
	}
	for _, p := range perms {
		ok, err := h.authz.Can(r.Context(), u, authz.Action(p), authz.Resource{TeamID: team})
		if err != nil {
			api.Fail(w, h.logger, u, "failed to check the caller's permissions", err)
			return false
		}
		if !ok {
			http.Error(w, "you may not let someone speak as a user with more access than yours",
				http.StatusForbidden)
			return false
		}
	}
	return true
}

// errMoved is the 409 for a binding that changed between the check and the
// write: what was checked is no longer what would be changed.
const errMoved = "the sender's binding changed while this was being decided; look again and retry"

// coversHolder applies covers to whoever the ref is bound to now, unless that
// is keep: removing a binding, or moving it to somebody else, takes it from
// them. A ref bound to nobody, or to somebody no longer in the team, holds
// nothing to protect.
//
// It returns the holder it read, nil for none, for the write to name: the
// check and the write are separate statements, and the write must refuse a
// binding that moved between them.
func (h *handler) coversHolder(
	w http.ResponseWriter, r *http.Request, u *auth.User, channel db.Channel, ref string, keep uuid.UUID,
) (*uuid.UUID, bool) {
	holder, err := h.store.FindChannelSender(r.Context(), db.FindChannelSenderParams{
		ChannelID: channel.ID, SenderRef: ref,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, true
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the sender's binding", err)
		return nil, false
	}
	if holder == keep {
		return &holder, true
	}

	role, err := h.store.FindTeamMember(r.Context(), db.FindTeamMemberParams{
		TeamID: channel.TeamID, UserID: holder,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return &holder, true
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to check team membership", err)
		return nil, false
	}
	return &holder, h.covers(w, r, u, channel.TeamID, role)
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

	holder, ok := h.coversHolder(w, r, u, channel, ref, uuid.Nil)
	if !ok {
		return
	}

	removed, err := h.store.RemoveSender(r.Context(), db.RemoveSenderParams{
		ChannelID: channel.ID, SenderRef: ref, ExpectedHolder: holder,
	})
	if err != nil {
		api.Fail(w, h.logger, u, "failed to remove sender", err)
		return
	}
	if !removed {
		http.Error(w, errMoved, http.StatusConflict)
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
