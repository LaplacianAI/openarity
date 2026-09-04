// Package ui serves the dashboard: one build, embedded in the brain binary,
// mounted under /ui.
//
// Under a prefix rather than at the root because the brain already owns
// /teams, /users, /auth and /healthz. A single-page app at "/" needs a
// catch-all, and a catch-all at the root has to know every API prefix in order
// not to swallow it — a list that goes stale the first time somebody adds an
// endpoint. Under /ui the catch-all knows nothing and cannot be wrong.
//
// Same origin as the API it calls, which is not incidental: package docs
// records what cross-origin costs here, and a browser reports it as a bare
// network failure with nothing useful in it.
package ui

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
)

// all: rather than a plain pattern, because on a fresh clone this directory
// holds only .gitkeep — a dotfile, which //go:embed skips by default, leaving
// "no matching files" and a brain that will not compile without Node having
// run first. The dashboard is build output and is not committed; keeping the
// directory embeddable while empty is what keeps `go build ./...` honest.
//
//go:embed all:dist
var bundle embed.FS

// http.ServeContent skips Last-Modified entirely for a zero time, which is
// what index.html wants: it is served no-store, so a validator would only
// invite a conditional request that can never be answered usefully.
var zeroTime time.Time

const (
	indexFile = "index.html"

	// Vite puts every fingerprinted file here, so this prefix is the boundary
	// between "the build produced it" and "the dashboard routes it".
	assetPrefix = "assets/"
)

type handler struct {
	logger *slog.Logger
	files  fs.FS
	server http.Handler
	built  bool
}

func New(logger *slog.Logger) *api.Router {
	h := newHandler(logger)

	// Public: the browser fetches the page and its scripts before it has a
	// token, so anything guarded here would 401 the login screen itself.
	r := api.NewPublicRouter("")
	r.Get("/{$}", h.toDashboard)
	r.Get("/ui", h.toDashboard)
	r.Get("/ui/", h.serve)
	return r
}

func newHandler(logger *slog.Logger) *handler {
	files, err := fs.Sub(bundle, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is
		// a build-time fact rather than a runtime one.
		panic("ui: the embedded bundle has no dist directory: " + err.Error())
	}

	h := &handler{logger: logger, files: files, server: http.FileServerFS(files)}
	h.built = exists(files, indexFile)

	if !h.built {
		logger.Warn("the dashboard was not built into this binary; /ui explains how",
			"remedy", "make ui")
	}
	return h
}

// The root is not a landing page — it is the one address somebody types from
// memory, and it should reach the dashboard rather than a 404.
func (h *handler) toDashboard(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	if !h.built {
		h.notBuilt(w)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/ui/")

	// Except under assets/, which the build owns entirely. A missing file there
	// is a missing file, never a client route, and answering it with index.html
	// hands the browser HTML where it asked for a module — reported as
	// "Expected a JavaScript module script but the server responded with a MIME
	// type of text/html", which says nothing about the stale index.html that
	// actually caused it. A 404 is the honest answer and the legible one.
	if !exists(h.files, name) && strings.HasPrefix(name, assetPrefix) {
		http.NotFound(w, r)
		return
	}

	// Anything else that is not a file the build produced is a route the
	// dashboard handles itself, so it gets index.html and the client router
	// reads the address. Without this a reload on /ui/teams is a 404 — the page
	// works until somebody presses F5, which is the worst way to find out.
	if name == "" || !exists(h.files, name) {
		h.index(w, r)
		return
	}

	// Vite fingerprints asset filenames, so a changed file is a changed URL and
	// this can be cached indefinitely. index.html below deliberately is not.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.StripPrefix("/ui/", h.server).ServeHTTP(w, r)
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	page, err := fs.ReadFile(h.files, indexFile)
	if err != nil {
		h.logger.Error("the dashboard bundle has no index.html", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// no-store rather than a validator: this document names the fingerprinted
	// assets, so a cached copy after a deploy asks for scripts that no longer
	// exist and the page fails with nothing on screen to explain it.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, indexFile, zeroTime, strings.NewReader(string(page)))
}

// A page rather than a 404, because the likely reader is a contributor who
// built the brain without building the dashboard, and a 404 tells them the
// route is wrong when the route is fine.
func (h *handler) notBuilt(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(notBuiltPage))
}

func exists(files fs.FS, name string) bool {
	f, err := files.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	return err == nil && !info.IsDir()
}

const notBuiltPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Openarity — dashboard not built</title>
</head>
<body>
  <h1>The dashboard is not in this binary</h1>
  <p>
    The API is running and unaffected — only this page is missing. The bundle
    is built separately and embedded at compile time:
  </p>
  <pre><code>cd apps/brain
make ui        # builds apps/dashboard into internal/api/ui/dist
go build ./...</code></pre>
  <p>Released builds do this in CI, so a published image always has it.</p>
</body>
</html>
`
