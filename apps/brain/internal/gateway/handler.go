// Package gateway is the brain's public inbound edge: channel adapters and
// the webhook handler that drives them. Nothing downstream learns which
// channel a message came from — see CLAUDE.md in this directory.
package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/LaplacianAI/openarity/apps/brain/internal/contracts"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
)

const (
	// maxBodyBytes bounds a webhook body. Telegram text tops out at 4096
	// UTF-16 code units, but an Update also carries reply_to_message (a full
	// nested message with its own text), entities, quotes and media
	// metadata, and non-ASCII text costs up to 4 bytes per character — so
	// the bound is generous for any legitimate update while still capping
	// what an attacker can make the handler hold.
	maxBodyBytes = 256 << 10

	// callTimeout bounds each dependency call so a hung secret store or sink
	// cannot pin handler goroutines — the server's WriteTimeout fails the
	// response but does not cancel the handler.
	callTimeout = 5 * time.Second

	// maxLoggedField caps attacker-controlled values before they reach a
	// log field.
	maxLoggedField = 64

	// maxLoggedError caps attached error strings — larger than
	// maxLoggedField because a store error needs room to be diagnosable,
	// but bounded because a backend error can embed URLs, response bodies
	// or attacker-authored message text.
	maxLoggedError = 256
)

// outcome is the closed set of values the exit log line's "outcome" field
// can carry. Operators query on this dimension, so a typo may not invent a
// new one.
type outcome string

const (
	outcomeDelivered outcome = "delivered"
	outcomeRejected  outcome = "rejected"
	outcomeDropped   outcome = "dropped"
	outcomeIgnored   outcome = "ignored"
	outcomeError     outcome = "error"
)

// reasonOversized is the one drop that is a silent permanent loss of a
// possibly legitimate message, so serve raises it above Info.
const reasonOversized = "oversized body"

type handler struct {
	logger   *slog.Logger
	adapter  contracts.WebhookAdapter
	channels map[string]string // channel id → owning tenant id
	store    secrets.Store
	sink     contracts.Sink
}

// New builds the webhook listener's handler for one adapter: that channel's
// route and nothing else. channels maps a public channel id to the tenant
// that owns it; New copies it, so a caller later mutating its map cannot
// race the per-request lookups.
func New(logger *slog.Logger, adapter contracts.WebhookAdapter, channels map[string]string, store secrets.Store, sink contracts.Sink) http.Handler {
	h := &handler{logger: logger, adapter: adapter, channels: maps.Clone(channels), store: store, sink: sink}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/"+adapter.Channel()+"/{channelID}", h.serve)
	return mux
}

// serve funnels every outcome through one exit log line. The middleware
// already logs method, path, status and duration; this line carries only
// what the middleware cannot know. The status is written before the log
// call so the ack never waits on the log sink.
func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	channelID := r.PathValue("channelID")
	status, out, reason, err := h.handle(w, r, channelID)
	w.WriteHeader(status)

	level := slog.LevelInfo
	switch {
	case out == outcomeError:
		// Transient infrastructure failures — a sealed Vault, a failing
		// sink — must be visible to level-based alerting.
		level = slog.LevelError
	case reason == reasonOversized:
		level = slog.LevelWarn
	}
	attrs := []any{
		"channel", h.adapter.Channel(),
		"channel_id", clipField(channelID, maxLoggedField),
		"outcome", string(out),
		"reason", clipField(reason, maxLoggedField),
	}
	if err != nil {
		attrs = append(attrs, "error", clipField(err.Error(), maxLoggedError))
	}
	h.logger.Log(r.Context(), level, "webhook", attrs...)
}

// handle runs read → lookup → secret → verify → parse → deliver and maps
// each failure to a status Telegram's retry loop can live with: 401 for
// authentication failures, 503 for transient faults worth redelivering, and
// 200 for payloads that will never be accepted — Telegram retries every
// non-2xx for 24 hours, so a permanent failure is acked and dropped rather
// than turned into a retry storm.
//
// The body is read before the channel lookup on purpose: the oversized and
// read-failure answers must not depend on whether the channel exists, or
// their status codes become an unauthenticated channel-enumeration oracle
// against the instant 401 an unknown channel gets.
//
// w is passed only so MaxBytesReader can mark the connection; serve owns the
// single WriteHeader, and nothing here may write a status or body. The
// returned error is attached to the exit log line and never sent to the
// caller.
func (h *handler) handle(w http.ResponseWriter, r *http.Request, channelID string) (status int, out outcome, reason string, err error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// The logging middleware's wrapper hides the writer from
			// net/http's oversize detection, so close the connection here —
			// otherwise net/http drains what was not read and keeps serving
			// the same connection.
			w.Header().Set("Connection", "close")
			return http.StatusOK, outcomeDropped, reasonOversized, nil
		}
		// A read failure past the size check is the connection dying, not
		// the payload — let the provider retry.
		return http.StatusServiceUnavailable, outcomeError, "body read failed", err
	}

	tenantID, ok := h.channels[channelID]
	if !ok {
		return http.StatusUnauthorized, outcomeRejected, "unknown channel", nil
	}

	path, err := secrets.ChannelPath(tenantID, channelID)
	if err != nil {
		// The registration itself is broken — fail closed like any other
		// auth failure and surface the cause in the log.
		return http.StatusUnauthorized, outcomeRejected, "unusable channel registration", err
	}

	getCtx, cancelGet := context.WithTimeout(r.Context(), callTimeout)
	defer cancelGet()
	secret, err := h.store.Get(getCtx, path)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return http.StatusUnauthorized, outcomeRejected, "no secret configured", nil
		}
		return http.StatusServiceUnavailable, outcomeError, "secret store unavailable", err
	}

	if err := h.adapter.Verify(r, body, secret); err != nil {
		if errors.Is(err, contracts.ErrSecretUnusable) {
			return http.StatusUnauthorized, outcomeRejected, "secret unusable", nil
		}
		return http.StatusUnauthorized, outcomeRejected, "verification failed", nil
	}

	msg, err := h.adapter.Parse(body)
	if err != nil {
		if errors.Is(err, contracts.ErrIgnore) {
			return http.StatusOK, outcomeIgnored, err.Error(), nil
		}
		return http.StatusOK, outcomeDropped, "malformed payload", nil
	}

	msg.Channel = h.adapter.Channel()
	msg.ChannelID = channelID
	msg.TenantID = tenantID

	// WithoutCancel: the provider hanging up must not abort a delivery that
	// is already in flight — at-least-once starts here.
	deliverCtx, cancelDeliver := context.WithTimeout(context.WithoutCancel(r.Context()), callTimeout)
	defer cancelDeliver()
	if err := h.sink.Deliver(deliverCtx, msg); err != nil {
		return http.StatusServiceUnavailable, outcomeError, "delivery failed", err
	}

	return http.StatusOK, outcomeDelivered, "", nil
}

// clipField caps a possibly attacker-controlled value for logging, cutting
// on a rune boundary so a multi-byte character is dropped whole rather than
// split into invalid UTF-8. The middleware package carries its own copy
// under the same name — depguard keeps these packages apart, and a shared
// name keeps the copies greppable.
func clipField(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
