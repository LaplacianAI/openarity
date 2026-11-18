// Package api is sandboxd's HTTP side: the rooms API, authenticated by one
// bearer token, and the reverse proxy to a room's ports, authenticated by a
// token of its own so the Authorization header passes through to the shim.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/rooms"
)

// ProxyHeader carries the room's proxy token. It is not Authorization,
// because the brain sends the shim's own bearer token there.
const ProxyHeader = "X-Sandboxd-Token"

const maxBody = 1 << 20

// Rooms is what the API needs of the rooms service.
type Rooms interface {
	Create(ctx context.Context, sp rooms.Spec) (string, error)
	Start(ctx context.Context, id string, argv []string) error
	Delete(ctx context.Context, id string) error
	Exists(ctx context.Context, id string) error
	Target(ctx context.Context, id string, port int) (string, error)
	Token(id string) string
}

// Handler serves the API.
type Handler struct {
	rooms     Rooms
	token     string
	publicURL string
	logger    *slog.Logger
	mux       *http.ServeMux

	mu      sync.Mutex
	targets map[string]cached
}

type cached struct {
	addr string
	at   time.Time
}

// targetTTL bounds how long a room's address is remembered: a room is on one
// address for its life, but a deleted room must stop being reachable soon.
const targetTTL = 5 * time.Second

// New returns the handler. publicURL is where the brain reaches sandboxd.
func New(r Rooms, token, publicURL string, logger *slog.Logger) *Handler {
	if logger == nil {
		panic("api: a nil logger")
	}
	h := &Handler{
		rooms: r, token: token, publicURL: strings.TrimRight(publicURL, "/"),
		logger: logger, mux: http.NewServeMux(), targets: map[string]cached{},
	}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h.mux.Handle("POST /v1/rooms", h.bearer(h.create))
	h.mux.Handle("POST /v1/rooms/{id}/start", h.bearer(h.start))
	h.mux.Handle("GET /v1/rooms/{id}/ports/{port}", h.bearer(h.port))
	h.mux.Handle("DELETE /v1/rooms/{id}", h.bearer(h.remove))
	h.mux.HandleFunc("/v1/rooms/{id}/ports/{port}/proxy", h.proxy)
	h.mux.HandleFunc("/v1/rooms/{id}/ports/{port}/proxy/{rest...}", h.proxy)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) bearer(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !equal(got, h.token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	})
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps a service error to a status.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, rooms.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such room")
	case errors.Is(err, rooms.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, "the sandbox is busy")
	case errors.Is(err, rooms.ErrUnavailable):
		// the message names the image, and the daemon's reason
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, rooms.ErrRefused):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, rooms.ErrStarted):
		writeError(w, http.StatusConflict, err.Error())
	default:
		h.logger.Error("request failed", "path", r.URL.Path, "err", err.Error())
		writeError(w, http.StatusInternalServerError, "the sandbox failed")
	}
}

// decode reads a JSON body, refusing a field it does not know: a request
// that names a mount or a capability is refused, not ignored.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "the body is not a valid request: "+err.Error())
		return false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusUnprocessableEntity, "the body holds more than one value")
		return false
	}
	return true
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var sp rooms.Spec
	if !decode(w, r, &sp) {
		return
	}
	id, err := h.rooms.Create(r.Context(), sp)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Argv []string `json:"argv"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := h.rooms.Start(r.Context(), r.PathValue("id"), body.Argv); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.rooms.Delete(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	h.forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func parsePort(s string) (int, bool) {
	p, err := strconv.Atoi(s)
	return p, err == nil && p >= 1 && p <= 65535
}

func (h *Handler) port(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := parsePort(r.PathValue("port"))
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "not a port")
		return
	}
	if err := h.rooms.Exists(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":     h.publicURL + "/v1/rooms/" + url.PathEscape(id) + "/ports/" + strconv.Itoa(p) + "/proxy",
		"headers": map[string]string{ProxyHeader: h.rooms.Token(id)},
	})
}

func (h *Handler) forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.targets {
		if strings.HasPrefix(k, id+":") {
			delete(h.targets, k)
		}
	}
}

func (h *Handler) target(ctx context.Context, id string, port int) (string, error) {
	key := id + ":" + strconv.Itoa(port)
	h.mu.Lock()
	c, ok := h.targets[key]
	h.mu.Unlock()
	if ok && time.Since(c.at) < targetTTL {
		return c.addr, nil
	}
	addr, err := h.rooms.Target(ctx, id, port)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	h.targets[key] = cached{addr: addr, at: time.Now()}
	h.mu.Unlock()
	return addr, nil
}

// proxy relays to a room's port. Everything but the proxy token passes
// through untouched, Authorization included.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !equal(r.Header.Get(ProxyHeader), h.rooms.Token(id)) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	p, ok := parsePort(r.PathValue("port"))
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "not a port")
		return
	}
	addr, err := h.target(r.Context(), id, p)
	if errors.Is(err, rooms.ErrNotStarted) {
		writeError(w, http.StatusBadGateway, "the room has not started")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rest := "/" + r.PathValue("rest")
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = addr
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = addr
			pr.Out.Header.Del(ProxyHeader)
			// Rewrite drops X-Forwarded-*, and Authorization is kept.
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.logger.Warn("proxy failed", "room", id, "port", p, "err", err.Error())
			h.forget(id)
			writeError(w, http.StatusBadGateway, "the room did not answer")
		},
	}
	rp.ServeHTTP(w, r)
}
