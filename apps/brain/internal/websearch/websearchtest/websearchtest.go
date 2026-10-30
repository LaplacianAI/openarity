// Package websearchtest is a stand-in search provider for the adapters'
// tests: an httptest server that answers every request alike and records
// what each one asked.
package websearchtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

// Asked is what one request carried.
type Asked struct {
	Method, Path, Auth, Token string
	Query                     map[string][]string
	Body                      map[string]any
}

type Server struct {
	*httptest.Server
	mu   sync.Mutex
	seen []Asked
}

// Serve answers every request with status and reply.
func Serve(t *testing.T, status int, reply string) *Server {
	t.Helper()

	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := Asked{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
			Auth: r.Header.Get("Authorization"), Token: r.Header.Get("X-Subscription-Token"),
		}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &a.Body)
		}
		s.mu.Lock()
		s.seen = append(s.seen, a)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Requests() []Asked {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Asked(nil), s.seen...)
}

// Client reaches the test server: loopback, which the guarded client refuses
// unless told otherwise.
func Client() *http.Client {
	return websearch.NewClient(func(netip.Addr) bool { return true })
}

// TwoHits is what every adapter's fixture reply holds, read back.
var TwoHits = []websearch.Hit{
	{Title: "Invoices due", URL: "https://a.example/due", Snippet: "Three are due"},
	{Title: "Billing", URL: "https://b.example/", Snippet: "How billing works"},
}
