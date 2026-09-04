package ui

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// The embedded bundle is whatever `make ui` last produced, so tests drive the
// handler against a filesystem they control. newHandler reads the embed;
// everything below builds the handler directly so both states — built and not
// — are reachable regardless of what is on disk.
func handlerFor(t *testing.T, files fs.FS) *handler {
	t.Helper()

	h := &handler{
		logger: slog.New(slog.DiscardHandler),
		files:  files,
		server: http.FileServerFS(files),
	}
	h.built = exists(files, indexFile)
	return h
}

func built() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>Openarity</title>")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h *handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)

	switch path {
	case "/", "/ui":
		h.toDashboard(w, req)
	default:
		h.serve(w, req)
	}
	return w
}

func TestTheRootRedirectsToTheDashboard(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/", "/ui"} {
		w := get(t, handlerFor(t, built()), path)
		if w.Code != http.StatusFound {
			t.Errorf("GET %s = %d, want %d", path, w.Code, http.StatusFound)
		}
		if got := w.Header().Get("Location"); got != "/ui/" {
			t.Errorf("GET %s redirected to %q, want /ui/", path, got)
		}
	}
}

func TestARealAssetIsServedWithItsContent(t *testing.T) {
	t.Parallel()

	w := get(t, handlerFor(t, built()), "/ui/assets/index-abc123.js")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "console.log(1)" {
		t.Errorf("body = %q, want the asset's bytes", w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want the immutable policy", got)
	}
}

// The reload case. A route the SPA owns has no file behind it, and a 404 here
// means the app works until somebody presses F5.
func TestAClientRouteFallsBackToIndex(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/ui/", "/ui/teams", "/ui/teams/42/sessions"} {
		w := get(t, handlerFor(t, built()), path)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
		if w.Body.String() != "<!doctype html><title>Openarity</title>" {
			t.Errorf("GET %s did not return index.html", path)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
}

// index.html names fingerprinted assets, so a cached copy after a deploy asks
// for scripts that no longer exist.
func TestIndexIsNeverCached(t *testing.T) {
	t.Parallel()

	w := get(t, handlerFor(t, built()), "/ui/")
	if got := w.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want none — no-store plus a validator is a contradiction", got)
	}
}

func TestAnUnbuiltBundleExplainsItself(t *testing.T) {
	t.Parallel()

	// What a fresh clone has: the directory is embeddable and holds nothing.
	h := handlerFor(t, fstest.MapFS{".gitkeep": {Data: []byte{}}})
	if h.built {
		t.Fatal("a bundle with no index.html reported itself as built")
	}

	w := get(t, h, "/ui/")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — the API is fine, only this page is missing", w.Code)
	}
	if body := w.Body.String(); !contains(body, "make ui") {
		t.Errorf("the page does not say how to fix it: %q", body)
	}
}

// A missing asset is a missing file, not a client route. Serving index.html
// here gives the browser HTML where it asked for a module, and the MIME error
// it reports says nothing about the stale index.html that caused it.
func TestAMissingAssetIsA404(t *testing.T) {
	t.Parallel()

	w := get(t, handlerFor(t, built()), "/ui/assets/index-deadbeef.js")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — assets/ is owned by the build, not the router", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want anything but a JavaScript type on a miss", ct)
	}
}

// A directory must not answer as a file, or `/ui/assets` would 200 with a
// listing of the bundle instead of falling through to the SPA.
func TestADirectoryIsNotAFile(t *testing.T) {
	t.Parallel()

	files := built()
	if exists(files, "assets") {
		t.Error("exists() reported a directory as a file")
	}

	w := get(t, handlerFor(t, files), "/ui/assets")
	if w.Body.String() != "<!doctype html><title>Openarity</title>" {
		t.Error("a directory path did not fall back to index.html")
	}
}

// The embed itself: whatever `make ui` last produced, the package must load.
func TestTheEmbeddedBundleIsReadable(t *testing.T) {
	t.Parallel()

	h := newHandler(slog.New(slog.DiscardHandler))
	if h.files == nil {
		t.Fatal("newHandler produced no filesystem")
	}

	// Both states are legitimate — CI builds the bundle, a Go-only contributor
	// does not — so this asserts consistency rather than one of them.
	if h.built != exists(h.files, indexFile) {
		t.Error("built disagrees with what the embedded filesystem holds")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
