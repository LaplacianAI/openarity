// Package workspace is a room's workspace: what an agent's Bash, Read, Write,
// Edit, Glob and Grep do, served over HTTP to the brain. It runs as the
// room's main process, so it works on any sandbox provider that can start a
// command in a room and give the address of a port in it.
//
// Every route is a POST of JSON behind the room's bearer token, and answers
// JSON. A mistake the model can fix, such as a path that is not there or an
// edit that matches twice, is 422 with a message for it to read; anything
// else wrong with the room is 500.
package workspace

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/roomfile"
)

// maxBody is the largest request read: a file written whole is the largest.
const maxBody = 16 << 20

// Handler serves one workspace.
type Handler struct {
	root   string
	token  string
	env    []string
	logger *slog.Logger
}

// NewHandler serves the workspace at root, which relative paths start from
// and every command runs in. env is the environment commands get; it must
// not hold the token.
func NewHandler(root, token string, env []string, logger *slog.Logger) *Handler {
	if logger == nil {
		panic("workspace: a nil logger")
	}
	return &Handler{root: root, token: token, env: env, logger: logger}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if info, err := os.Stat(h.root); err != nil || !info.IsDir() {
			http.Error(w, "the workspace directory is missing", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path == "/file" {
		if !h.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		roomfile.Serve(w, r, h.root, h.logger)
		return
	}
	route, ok := h.routes()[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	out, err := route(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = encoder(w).Encode(out)
}

// encoder writes JSON without escaping <, > and &: an answer is read by the
// brain, not a page, and code is full of them, each six bytes escaped.
func encoder(w http.ResponseWriter) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

type route func(*http.Request) (any, error)

func (h *Handler) routes() map[string]route {
	return map[string]route{
		"/exec":  h.exec,
		"/read":  h.read,
		"/write": h.write,
		"/edit":  h.edit,
		"/glob":  h.glob,
		"/grep":  h.grep,
	}
}

func (h *Handler) exec(r *http.Request) (any, error) {
	var in struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, problem("command is empty")
	}
	timeout := DefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, MaxTimeout)
	}
	return run(r.Context(), h.root, h.env, in.Command, timeout), nil
}

func (h *Handler) read(r *http.Request) (any, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	p, err := h.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	return read(p, in.Offset, in.Limit)
}

func (h *Handler) write(r *http.Request) (any, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	p, err := h.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	n, err := write(p, in.Content)
	return map[string]any{"path": p, "bytes": n}, err
}

func (h *Handler) edit(r *http.Request) (any, error) {
	var in struct {
		Path       string `json:"path"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	p, err := h.resolve(in.Path)
	if err != nil {
		return nil, err
	}
	n, err := edit(p, in.Old, in.New, in.ReplaceAll)
	return map[string]any{"path": p, "replaced": n}, err
}

func (h *Handler) glob(r *http.Request) (any, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	base, err := h.base(in.Path)
	if err != nil {
		return nil, err
	}
	return glob(base, in.Pattern)
}

func (h *Handler) grep(r *http.Request) (any, error) {
	var q GrepQuery
	if err := decode(r, &q); err != nil {
		return nil, err
	}
	if q.Pattern == "" {
		return nil, problem("pattern is empty")
	}
	base, err := h.base(q.Path)
	if err != nil {
		return nil, err
	}
	q.Path = base
	return grep(q)
}

// resolve is the absolute path a caller means: relative to the workspace,
// or as given, with its links followed. The room is the boundary, not the
// workspace directory, with one exception: /proc. These tools run in the
// workspace process, which can always read its own environ, where the token
// it was started with stays; Harden keeps it from everything else.
func (h *Handler) resolve(p string) (string, error) { return roomfile.Resolve(h.root, p) }

// base is where a search starts: the workspace when no path is given. It is
// the path as given, so what is found is named as the caller named it, but
// for a base that is itself a link; the walk follows no link, and skips
// /proc.
func (h *Handler) base(p string) (string, error) {
	if p == "" {
		return h.root, nil
	}
	if _, err := h.resolve(p); err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(h.root, p)
	}
	p = filepath.Clean(p)
	// The walk follows no link, so a base that is one starts at its target.
	if info, err := os.Lstat(p); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return h.resolve(p)
	}
	return p, nil
}

func (h *Handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && h.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// decode reads the body into v, refusing fields v does not have: a caller
// that misspells one learns of it rather than having it ignored.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return problem("the request is over %d MiB", maxBody>>20)
		}
		return problem("the request is not the JSON this route takes: %v", err)
	}
	return nil
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "application/json")
	var p *Problem
	if errors.As(err, &p) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = encoder(w).Encode(map[string]string{"error": p.Error()})
		return
	}
	h.logger.Error("workspace: request failed", "path", r.URL.Path, "error", err)
	w.WriteHeader(http.StatusInternalServerError)
	_ = encoder(w).Encode(map[string]string{"error": "the workspace failed: " + err.Error()})
}
