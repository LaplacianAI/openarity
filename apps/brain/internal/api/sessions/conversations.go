package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Deliverer is the inbox the webhooks deliver to: what a person types on the
// dashboard is a message on its channel, stored and answered the same way.
type Deliverer interface {
	DeliverOne(ctx context.Context, ch gateway.Channel, m gateway.Delivery) (gateway.Delivered, error)
}

// Files stores what a person sends with a message, as the webhooks' files are
// stored: its type read from its bytes, its key reserved before it is written.
type Files interface {
	Store(ctx context.Context, teamID uuid.UUID, filename string, body []byte) (gateway.Stored, error)
}

const (
	// maxSaid is the most a message may say, in bytes: what a run may be asked.
	maxSaid = 100 << 10
	// maxFiles is the most files one message may carry, and maxUpload the
	// most a message with files may be in all.
	maxFiles  = 8
	maxUpload = maxFiles*objects.MaxAttachment + 1<<20
)

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
	req, ok := h.readSay(w, r, teamID)
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
	req, ok := h.readSay(w, r, teamID)
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
		Files:  req.files,
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

// readSay reads what was said, as JSON or, with files, as multipart: some
// text or at least one file, no NUL, no more than a run may be asked, and a
// key.
func (h *handler) readSay(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (sayRequest, bool) {
	var req sayRequest
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind == "multipart/form-data" {
		var ok bool
		if req, ok = h.readUpload(w, r, teamID); !ok {
			return sayRequest{}, false
		}
	} else if !api.DecodeJSON(w, r, &req) {
		return sayRequest{}, false
	}

	switch {
	case strings.TrimSpace(req.Text) == "" && len(req.files) == 0:
		http.Error(w, "text must say something, or a file be sent", http.StatusBadRequest)
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

// readUpload reads a message sent with files one part at a time, so no more
// than one file is held at once. Each file is stored as it is read; one
// refused after others were stored leaves those to the reaper, since their
// keys were reserved before they were written and no row will name them.
func (h *handler) readUpload(w http.ResponseWriter, r *http.Request, teamID uuid.UUID) (sayRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	parts, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "the body is not multipart/form-data", http.StatusBadRequest)
		return sayRequest{}, false
	}

	var req sayRequest
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			return req, true
		}
		if err != nil {
			uploadFailed(w, err)
			return sayRequest{}, false
		}

		switch part.FormName() {
		case "text":
			if req.Text, err = field(part, maxSaid); err != nil {
				uploadFailed(w, err)
				return sayRequest{}, false
			}
		case "key":
			if req.Key, err = field(part, 64); err != nil {
				uploadFailed(w, err)
				return sayRequest{}, false
			}
		case "files":
			if len(req.files) == maxFiles {
				http.Error(w, fmt.Sprintf("a message may carry at most %d files", maxFiles), http.StatusBadRequest)
				return sayRequest{}, false
			}
			stored, ok := h.storeFile(w, r, teamID, part)
			if !ok {
				return sayRequest{}, false
			}
			req.files = append(req.files, stored)
		default:
			http.Error(w, fmt.Sprintf("the body has a part named %q; it takes text, key and files", part.FormName()), http.StatusBadRequest)
			return sayRequest{}, false
		}
	}
}

func (h *handler) storeFile(w http.ResponseWriter, r *http.Request, teamID uuid.UUID, part *multipart.Part) (gateway.Stored, bool) {
	body, err := io.ReadAll(io.LimitReader(part, objects.MaxAttachment+1))
	if err != nil {
		uploadFailed(w, err)
		return gateway.Stored{}, false
	}
	stored, err := h.files.Store(r.Context(), teamID, part.FileName(), body)
	var refused *gateway.Refused
	if errors.As(err, &refused) {
		http.Error(w, refused.Reason, http.StatusBadRequest)
		return gateway.Stored{}, false
	}
	if err != nil {
		api.Fail(w, h.logger, api.Caller(r), "failed to store a file", err)
		return gateway.Stored{}, false
	}
	return stored, true
}

// field reads a part that is not a file, no more than a byte past its limit:
// enough for the checks after it to refuse one that is too long.
func field(part *multipart.Part, limit int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, limit+1))
	return string(b), err
}

// uploadFailed answers a body that could not be read: past its cap, or not
// well formed.
func uploadFailed(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, fmt.Sprintf("a message with its files must be at most %d MB", maxUpload>>20), http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "the body could not be read: "+err.Error(), http.StatusBadRequest)
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
