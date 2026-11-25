package roomfile

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
)

// room answers /file as the handler says, recording the path and token it was
// asked with.
func room(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) sandbox.Endpoint {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	return sandbox.Endpoint{URL: srv.URL + Route, Header: http.Header{"Authorization": {"Bearer tok"}}}
}

// The file comes back whole, asked for by its path, with the room's token.
func TestAFileIsReadWhole(t *testing.T) {
	t.Parallel()

	var path, auth string
	ep := room(t, func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Query().Get("path"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte("%PDF-1.7\n"))
	})
	got, err := Get(t.Context(), http.DefaultClient, ep, "mail/a b&c.pdf")
	if err != nil || string(got) != "%PDF-1.7\n" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if path != "mail/a b&c.pdf" || auth != "Bearer tok" {
		t.Errorf("asked for %q with %q", path, auth)
	}
}

// The room's own refusal is the model's to read, whatever its status; a 404
// the room did not mark is the provider's, for a room it no longer has.
func TestARefusalIsTheRoomsOrTheProviders(t *testing.T) {
	t.Parallel()

	refused := room(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(problemHeader, "1")
		http.Error(w, "mail/x.pdf does not exist", http.StatusNotFound)
	})
	_, err := Get(t.Context(), http.DefaultClient, refused, "mail/x.pdf")
	var p *Problem
	if !errors.As(err, &p) || p.Msg != "mail/x.pdf does not exist" {
		t.Errorf("a refusal = %v, want the room's words", err)
	}

	gone := room(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	if _, err := Get(t.Context(), http.DefaultClient, gone, "x"); !errors.Is(err, ErrGone) {
		t.Errorf("an unmarked 404 = %v, want ErrGone", err)
	}
}

// Nothing listening is a room that is gone.
func TestNobodyListeningIsAGoneRoom(t *testing.T) {
	t.Parallel()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	_, err = Get(t.Context(), http.DefaultClient, sandbox.Endpoint{URL: "http://" + addr + Route}, "x")
	if !errors.Is(err, ErrGone) {
		t.Errorf("Get = %v, want ErrGone", err)
	}
}

// A file over the most kept is refused, by its length before it is read and
// by what it sends if its length lied; one shorter than it said is a failure,
// which net/http reports, never the part of it that came.
func TestAFileTooLargeOrCutShortIsRefused(t *testing.T) {
	t.Parallel()

	said := room(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "26214401")
		w.WriteHeader(http.StatusOK)
	})
	var p *Problem
	if _, err := Get(t.Context(), http.DefaultClient, said, "big.zip"); !errors.As(err, &p) || !strings.Contains(p.Msg, "26214401") {
		t.Errorf("a length over the most = %v", err)
	}

	sent := room(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		_, _ = w.Write(make([]byte, Max+1))
	})
	if _, err := Get(t.Context(), http.DefaultClient, sent, "big.zip"); !errors.As(err, &p) {
		t.Errorf("a body over the most = %v", err)
	}

	short := room(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abc"))
	})
	if _, err := Get(t.Context(), http.DefaultClient, short, "x"); err == nil || errors.As(err, &p) {
		t.Errorf("a body cut short = %v, want a failure of the room's", err)
	}
}

// The token never leaves in an error: the address can carry the provider's.
func TestAnErrorNamesNoAddress(t *testing.T) {
	t.Parallel()

	ep := room(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	ep.URL = strings.Replace(ep.URL, "http://", "http://secret-key@", 1)
	_, err := Get(t.Context(), http.DefaultClient, ep, "x")
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("Get = %v", err)
	}
}
