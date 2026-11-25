package roomfile

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func get(t *testing.T, root, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/file?path="+url.QueryEscape(path), nil)
	Serve(w, r, root, quiet)
	return w
}

// A file is handed over whole, with its length, from a path relative to the
// room's root or absolute.
func TestAFileIsHandedOverWhole(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mail"), 0o750); err != nil {
		t.Fatal(err)
	}
	want := "%PDF-1.7\n"
	if err := os.WriteFile(filepath.Join(root, "mail", "invoice.pdf"), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"mail/invoice.pdf", filepath.Join(root, "mail", "invoice.pdf")} {
		w := get(t, root, path)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Length") != strconv.Itoa(len(want)) || w.Header().Get("Content-Type") != "application/octet-stream" {
			t.Errorf("%s: headers %v", path, w.Header())
		}
	}
}

// What cannot be handed over says why, by the status that fits: missing is
// 404, too large 413 with its size, anything else 422.
func TestAFileThatCannotBeHandedOverSaysWhy(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxDownload + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	exact, err := os.Create(filepath.Join(root, "exact.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := exact.Truncate(MaxDownload); err != nil {
		t.Fatal(err)
	}
	_ = exact.Close()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		path   string
		status int
	}{
		"missing":          {"nothing.pdf", http.StatusNotFound},
		"too large":        {"big.zip", http.StatusRequestEntityTooLarge},
		"exactly the most": {"exact.zip", http.StatusOK},
		"a directory":      {".", http.StatusUnprocessableEntity},
		"a fifo":           {"pipe", http.StatusUnprocessableEntity},
		"no path":          {"", http.StatusUnprocessableEntity},
		"under /proc":      {"/proc/self/environ", http.StatusUnprocessableEntity},
		"/proc by ..":      {root + "/../../../../../../../../proc/self/environ", http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			w := get(t, root, c.path)
			if w.Code != c.status {
				t.Fatalf("%d %q, want %d", w.Code, w.Body.String(), c.status)
			}
			if refused := w.Header().Get(ProblemHeader) == "1"; refused != (c.status != http.StatusOK) {
				t.Errorf("marked as the room's own refusal: %v", refused)
			}
		})
	}
	if w := get(t, root, "big.zip"); !strings.Contains(w.Body.String(), strconv.Itoa(MaxDownload+1)) {
		t.Errorf("a file too large does not say its size: %q", w.Body.String())
	}
}

// A link is followed to what it names, and the rules hold there: a link into
// /proc is refused as /proc is.
func TestALinkIsJudgedByWhatItNames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if w := get(t, root, "alias.txt"); w.Code != http.StatusOK || w.Body.String() != "hi" {
		t.Errorf("a link: %d %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc here")
	}
	if err := os.Symlink("/proc/self/environ", filepath.Join(root, "env")); err != nil {
		t.Fatal(err)
	}
	if w := get(t, root, "env"); w.Code == http.StatusOK {
		t.Errorf("a link into /proc was handed over: %q", w.Body.String())
	}
}

// Only GET: nothing else is a download.
func TestOnlyAGetIsADownload(t *testing.T) {
	w := httptest.NewRecorder()
	Serve(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/file?path=x", nil), t.TempDir(), quiet)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET" {
		t.Errorf("%d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
}
