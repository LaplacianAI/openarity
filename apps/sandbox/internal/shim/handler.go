package shim

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/roomfile"
)

// maxBody is the largest request the shim reads: a tool call's arguments,
// never a file.
const maxBody = 4 << 20

const sessionHeader = "Mcp-Session-Id"

// Handler serves the shared server over Streamable HTTP at /mcp, answering
// each request with JSON rather than a stream: the protocol allows it, and
// nothing here pushes.
type Handler struct {
	child  *Child
	token  string
	logger *slog.Logger
	// root is where a relative path for /file starts: the directory the
	// server was started in, where it writes what it saves.
	root     string
	sessions *sessions
}

// NewHandler serves the server child as MCP at /mcp, and the room's files at
// /file, from root, both behind token.
func NewHandler(child *Child, token, root string, logger *slog.Logger) *Handler {
	if logger == nil {
		panic("shim: a nil logger")
	}
	return &Handler{child: child, token: token, root: root, logger: logger, sessions: newSessions(time.Now)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		h.health(w)
		return
	}
	if r.URL.Path != "/mcp" && r.URL.Path != "/file" {
		http.NotFound(w, r)
		return
	}
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/file" {
		// A file the server wrote in this room, which it can read itself.
		roomfile.Serve(w, r, h.root, h.logger)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.post(w, r)
	case http.MethodDelete:
		if !h.sessions.end(r.Header.Get(sessionHeader)) {
			http.Error(w, "no such session", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "nothing streams from this server", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) health(w http.ResponseWriter) {
	select {
	case <-h.child.Done():
		http.Error(w, "the MCP server has exited", http.StatusServiceUnavailable)
		return
	default:
	}
	if h.child.Initialized() == nil {
		http.Error(w, "the MCP server is starting", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	var m message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&m); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "the body is not one JSON-RPC message", http.StatusBadRequest)
		return
	}

	switch m.Method {
	case "initialize":
		h.initialize(w, m)
		return
	case "server/discover":
		// The stateless handshake; a client falls back to initialize.
		reply(w, m.ID, Response{Error: notFound})
		return
	}

	session := r.Header.Get(sessionHeader)
	switch {
	case session == "":
		http.Error(w, "Mcp-Session-Id is required: initialize first", http.StatusBadRequest)
		return
	case !h.sessions.live(session):
		http.Error(w, "no such session: initialize again", http.StatusNotFound)
		return
	}

	switch {
	case m.Method == "":
		// An answer to a request: the shim never sends one to a client.
		w.WriteHeader(http.StatusAccepted)
	case m.ID == nil:
		h.notification(w, session, m)
	case m.Method == "ping":
		reply(w, m.ID, Response{Result: json.RawMessage(`{}`)})
	default:
		h.call(w, r, session, m)
	}
}

func (h *Handler) initialize(w http.ResponseWriter, m message) {
	result := h.child.Initialized()
	if result == nil {
		http.Error(w, "the MCP server is starting", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set(sessionHeader, h.sessions.start())
	reply(w, m.ID, Response{Result: result})
}

// notification passes on a cancellation, under the server's id for the
// request; the rest concern the shim's own session with the server, which
// is already initialized.
func (h *Handler) notification(w http.ResponseWriter, session string, m message) {
	if m.Method == "notifications/cancelled" {
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			if id, ok := h.sessions.server(session, string(p.RequestID)); ok {
				_ = h.child.Cancel(id)
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) call(w http.ResponseWriter, r *http.Request, session string, m message) {
	request := string(m.ID)
	defer h.sessions.done(session, request)
	res, err := h.child.Call(r.Context(), m.Method, m.Params, func(id int64) {
		h.sessions.sent(session, request, id)
	})
	switch {
	case errors.Is(err, ErrExited):
		h.logger.Warn("call failed: the MCP server has exited", "method", m.Method)
		http.Error(w, "the MCP server has exited", http.StatusBadGateway)
	case err != nil:
		// The caller went away; there is no one to answer.
	default:
		reply(w, m.ID, res)
	}
}

func reply(w http.ResponseWriter, id json.RawMessage, r Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Response
	}{"2.0", id, r})
}

var notFound = json.RawMessage(`{"code":-32601,"message":"method not found"}`)
