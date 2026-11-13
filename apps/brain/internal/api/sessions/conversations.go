package sessions

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Deliverer is the inbox the webhooks deliver to: what a person types on the
// dashboard is a message on its channel, stored and answered the same way.
type Deliverer interface {
	DeliverOne(ctx context.Context, ch gateway.Channel, m gateway.Delivery) (gateway.Delivered, error)
}

// maxSaid is the most a message may say, in bytes: what a run may be asked.
const maxSaid = 100 << 10

// keyPattern is the key a client sends a message under, so a retry of the
// same send is stored once.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// startConversation is a person beginning a conversation on the dashboard: a
// new direct session of theirs, and its first message.
func (h *handler) startConversation(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	req, ok := h.readSay(w, r)
	if !ok {
		return
	}
	// Named by the caller and their key, so a retry of the same start finds
	// the conversation it began rather than beginning another.
	h.say(w, r, teamID, u.ID.String()+":"+req.Key, req, http.StatusCreated)
}

// sendMessage is a person writing in a conversation of theirs on the
// dashboard. Nobody writes in another's, and a channel's conversation is
// written in on that channel.
func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	u := api.Caller(r)
	teamID, ok := api.RequireTeam(w, r, h.logger)
	if !ok {
		return
	}
	session, ok := h.visible(w, r, teamID)
	if !ok {
		return
	}
	ch, err := h.store.DashboardChannel(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the dashboard channel", err)
		return
	}
	if session.ChannelID == nil || *session.ChannelID != ch.ID || session.ProviderRef == nil {
		http.Error(w, "this conversation is not one on the dashboard; write in it where it is held", http.StatusConflict)
		return
	}
	if session.UserID == nil || *session.UserID != u.ID {
		http.Error(w, "only the person this conversation is with may write in it", http.StatusForbidden)
		return
	}
	req, ok := h.readSay(w, r)
	if !ok {
		return
	}
	h.say(w, r, teamID, *session.ProviderRef, req, http.StatusAccepted)
}

// say delivers what the caller said to the conversation ref names on the
// team's dashboard channel, and answers what became of it. A key sent before
// is the same message again: it is answered with what was stored, 200.
func (h *handler) say(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, ref string, req sayRequest, created int) {
	u := api.Caller(r)
	ch, err := h.store.DashboardChannel(r.Context(), teamID)
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the dashboard channel", err)
		return
	}
	// A key is the caller's: scoped to them, one person's key never collides
	// with another's message.
	external := u.ID.String() + ":" + req.Key
	d, err := h.inbox.DeliverOne(r.Context(), gateway.Channel{ID: ch.ID, TeamID: teamID}, gateway.Delivery{
		Inbound: gateway.Inbound{
			ExternalID: external,
			Author:     gateway.Author{Ref: u.ID.String(), DisplayName: nameOf(u)},
			Session:    gateway.Session{Ref: ref, Kind: gateway.SessionDirect},
			Text:       req.Text,
		},
		UserID: u.ID,
	})
	if errors.Is(err, gateway.ErrTeamDeleted) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to deliver the message", err)
		return
	}
	if d.Message == uuid.Nil {
		h.alreadySaid(w, r, u, ch.ID, external)
		return
	}
	api.WriteJSON(w, h.logger, created, saidView(d))
}

func (h *handler) alreadySaid(w http.ResponseWriter, r *http.Request, u *auth.User, channelID uuid.UUID, external string) {
	m, err := h.store.GetMessageByExternalID(r.Context(), db.GetMessageByExternalIDParams{
		ChannelID: channelID, ExternalID: external,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Stored and gone between the two reads: the conversation was deleted.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		api.Fail(w, h.logger, u, "failed to read the message sent before", err)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, said{SessionID: m.SessionID, MessageID: m.ID})
}

// readSay decodes and checks what was said: some text, no NUL, no more than a
// run may be asked, and a key.
func (h *handler) readSay(w http.ResponseWriter, r *http.Request) (sayRequest, bool) {
	var req sayRequest
	if !api.DecodeJSON(w, r, &req) {
		return sayRequest{}, false
	}
	switch {
	case strings.TrimSpace(req.Text) == "":
		http.Error(w, "text must say something", http.StatusBadRequest)
	case len(req.Text) > maxSaid:
		http.Error(w, "text must be at most 100 KiB", http.StatusBadRequest)
	case strings.ContainsRune(req.Text, 0):
		http.Error(w, "text must not contain a NUL character", http.StatusBadRequest)
	case !keyPattern.MatchString(req.Key):
		http.Error(w, "key must be 1 to 64 letters, digits, - or _", http.StatusBadRequest)
	default:
		return req, true
	}
	return sayRequest{}, false
}

func nameOf(u *auth.User) string {
	if u.Email != nil && *u.Email != "" {
		return *u.Email
	}
	return u.Subject
}

func saidView(d gateway.Delivered) said {
	out := said{SessionID: d.Session, MessageID: d.Message, Unanswered: d.Unanswered}
	switch {
	case d.Run != uuid.Nil:
		run := d.Run
		out.RunID = &run
	case d.Steered != uuid.Nil:
		run := d.Steered
		out.RunID, out.Steered = &run, true
	}
	return out
}
