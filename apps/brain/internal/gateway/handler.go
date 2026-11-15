package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const maxBody = 1 << 20

type Channels interface {
	GetChannel(ctx context.Context, id uuid.UUID) (db.Channel, error)
}

type Store interface {
	Channels
	Senders
}

type Secrets interface {
	Get(ctx context.Context, path, key string) (string, error)
}

// Kicker has a notice sent now rather than at the next sweep. A kick that
// fails is only late: the sweep sends it within a minute.
type Kicker interface {
	KickNotice(ctx context.Context, noticeID int64) error
}

type handler struct {
	logger   *slog.Logger
	store    Store
	secrets  Secrets
	reg      Registry
	ingest   *Ingest
	receiver *Receiver
}

func New(
	logger *slog.Logger, s Store, sec Secrets, reg Registry, sink Sink, ingest *Ingest, kick Kicker,
) *api.Router {
	h := &handler{
		logger: logger, store: s, secrets: sec, reg: reg, ingest: ingest,
		receiver: NewReceiver(logger, s, reg, sink, kick),
	}

	r := api.NewPublicRouter("/hooks")
	for _, name := range reg.Names() {
		p, _ := reg.Get(name)
		for _, rt := range p.Routes() {
			r.Handle(rt.Method, "/"+name+"/{channel_id}"+rt.Suffix, h.hook(p, rt))
		}
	}
	return r
}

func (h *handler) hook(p Provider, rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		receivedAt := time.Now()

		body, ok := readBody(w, r)
		if !ok {
			return
		}

		channel, ok := h.channel(w, r, p)
		if !ok {
			return
		}

		creds, ok := h.credentials(w, r, p, channel)
		if !ok {
			return
		}

		req := WebhookRequest{
			Suffix:     rt.Suffix,
			Method:     r.Method,
			Header:     r.Header,
			Query:      r.URL.Query(),
			Body:       body,
			ReceivedAt: receivedAt,
		}

		if err := p.Verify(req, creds); err != nil {
			h.logger.Warn("gateway: signature rejected",
				"channel_id", channel.ID, "provider", p.Name(), "error", err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		result, err := p.Parse(req, creds)
		if err != nil {
			h.logger.Warn("gateway: parse failed",
				"channel_id", channel.ID, "provider", p.Name(), "error", err)
			ack(w, nil)
			return
		}

		files := func(ctx context.Context, claimed []Attachment) ([]Stored, []string) {
			return h.ingest.Fetch(ctx, p, req, creds, channel.TeamID, claimed)
		}
		err = h.receiver.Receive(r.Context(), p, channel, result.Messages, files)
		if errors.Is(err, ErrTeamDeleted) {
			h.logger.Info("gateway: delivery refused, the team is deleted", "channel_id", channel.ID)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err != nil {
			h.logger.Error("gateway: deliver", "channel_id", channel.ID, "error", err)
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		}

		ack(w, result.Ack)
	}
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err == nil {
		return body, true
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	http.Error(w, "unreadable body", http.StatusBadRequest)
	return nil, false
}

func (h *handler) channel(w http.ResponseWriter, r *http.Request, p Provider) (db.Channel, bool) {
	channelID, err := uuid.Parse(r.PathValue("channel_id"))
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return db.Channel{}, false
	}

	channel, err := h.store.GetChannel(r.Context(), channelID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "forbidden", http.StatusForbidden)
		return db.Channel{}, false
	case err != nil:
		h.logger.Error("gateway: read channel", "channel_id", channelID, "error", err)
		http.Error(w, "try again", http.StatusInternalServerError)
		return db.Channel{}, false
	}

	if channel.Provider != p.Name() {
		h.logger.Warn("gateway: channel posted to another provider's path",
			"channel_id", channelID, "channel_provider", channel.Provider, "path_provider", p.Name())
		http.Error(w, "forbidden", http.StatusForbidden)
		return db.Channel{}, false
	}

	return channel, true
}

func (h *handler) credentials(
	w http.ResponseWriter, r *http.Request, p Provider, channel db.Channel,
) (Credentials, bool) {
	path := secrets.Path(channel.TeamID, secrets.KindChannel, channel.ID)

	creds := make(Credentials, len(p.Keys()))
	for _, key := range p.Keys() {
		value, err := h.secrets.Get(r.Context(), path, key)
		if err != nil {
			h.logger.Error("gateway: credential unavailable",
				"channel_id", channel.ID, "key", key, "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return nil, false
		}
		creds[key] = value
	}

	return creds, true
}

func ack(w http.ResponseWriter, body []byte) {
	if len(body) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
