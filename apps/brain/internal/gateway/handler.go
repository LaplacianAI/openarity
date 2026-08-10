package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	// maxBodyBytes bounds a webhook body. Telegram text tops out at 4096
	// UTF-16 code units and captions at 1024, so a real update never comes
	// close.
	maxBodyBytes = 64 << 10

	// callTimeout bounds each dependency call so a hung secret store or sink
	// cannot pin handler goroutines — the server's WriteTimeout fails the
	// response but does not cancel the handler.
	callTimeout = 5 * time.Second

	// maxLoggedID caps attacker-controlled path values before they reach a
	// log field.
	maxLoggedID = 64
)

type handler struct {
	logger   *slog.Logger
	adapter  contracts.WebhookAdapter
	channels map[string]string // channel id → owning tenant id
	store    secrets.SecretStore
	sink     contracts.Sink
}

// New builds the webhook listener's handler: every registered channel route
// and nothing else. channels maps a public channel id to the tenant that
// owns it; it is read-only after this call.
func New(logger *slog.Logger, adapter contracts.WebhookAdapter, channels map[string]string, store secrets.SecretStore, sink contracts.Sink) http.Handler {
	h := &handler{logger: logger, adapter: adapter, channels: channels, store: store, sink: sink}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/telegram/{channelID}", h.serve)
	return mux
}

// serve funnels every outcome through one exit log line. The middleware
// already logs method, path, status and duration; this line carries only
// what the middleware cannot know.
func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	status, outcome, reason := h.handle(w, r, channelID)

	h.logger.Info("webhook",
		"channel", h.adapter.Channel(),
		"channel_id", clip(channelID),
		"outcome", outcome,
		"reason", reason,
	)
	w.WriteHeader(status)
}

// handle runs lookup → read → secret → verify → parse → deliver and maps
// each failure to a status Telegram's retry loop can live with: 401 for
// authentication failures, 503 for transient faults worth redelivering, and
// 200 for payloads that will never be accepted — Telegram retries every
// non-2xx for 24 hours, so a permanent failure is acked and dropped rather
// than turned into a retry storm.
func (h *handler) handle(w http.ResponseWriter, r *http.Request, channelID string) (status int, outcome, reason string) {
	tenantID, ok := h.channels[channelID]
	if !ok {
		return http.StatusUnauthorized, "rejected", "unknown channel"
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return http.StatusOK, "dropped", "oversized body"
		}
		// A read failure past the size check is the connection dying, not
		// the payload — let the provider retry.
		return http.StatusServiceUnavailable, "error", "body read failed"
	}

	getCtx, cancelGet := context.WithTimeout(r.Context(), callTimeout)
	defer cancelGet()
	secret, err := h.store.Get(getCtx, secrets.ChannelPath(tenantID, channelID))
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return http.StatusUnauthorized, "rejected", "no secret configured"
		}
		return http.StatusServiceUnavailable, "error", "secret store unavailable"
	}

	if err := h.adapter.Verify(r, body, secret); err != nil {
		if errors.Is(err, errSecretUnusable) {
			return http.StatusUnauthorized, "rejected", "secret unusable"
		}
		return http.StatusUnauthorized, "rejected", "verification failed"
	}

	msg, err := h.adapter.Parse(body)
	if err != nil {
		if errors.Is(err, contracts.ErrIgnore) {
			return http.StatusOK, "ignored", err.Error()
		}
		return http.StatusOK, "dropped", "malformed payload"
	}

	msg.Channel = h.adapter.Channel()
	msg.ChannelID = channelID
	msg.TenantID = tenantID

	// WithoutCancel: the provider hanging up must not abort a delivery that
	// is already in flight — at-least-once starts here.
	deliverCtx, cancelDeliver := context.WithTimeout(context.WithoutCancel(r.Context()), callTimeout)
	defer cancelDeliver()
	if err := h.sink.Deliver(deliverCtx, msg); err != nil {
		return http.StatusServiceUnavailable, "error", "delivery failed"
	}

	return http.StatusOK, "delivered", ""
}

func clip(s string) string {
	if len(s) > maxLoggedID {
		return s[:maxLoggedID]
	}
	return s
}
