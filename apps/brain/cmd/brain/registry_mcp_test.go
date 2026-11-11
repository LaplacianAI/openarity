package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
)

func TestTheMCPRegistryCanBeTurnedOff(t *testing.T) {
	t.Parallel()
	if c := newMCPRegistry(&config.Config{MCPRegistryURL: config.NoMCPRegistry}); c != nil {
		t.Errorf("got a registry; want none")
	}
}

// The registry is dialled as web search is: a loopback registry is refused
// unless the operator lists its network, and a redirect is never followed.
func TestTheMCPRegistryReachesOnlyWhatMCPServersMay(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	t.Cleanup(srv.Close)

	refused := newMCPRegistry(&config.Config{MCPRegistryURL: srv.URL})
	if _, err := refused.Search(t.Context(), "x", 1); err == nil {
		t.Error("a loopback registry was reached with no network allowed")
	}

	allowed := newMCPRegistry(&config.Config{
		MCPRegistryURL:     srv.URL,
		MCPPrivateNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	if _, err := allowed.Search(t.Context(), "x", 1); err != nil {
		t.Errorf("an allowed registry: %v", err)
	}

	moved := httptest.NewServer(http.RedirectHandler(srv.URL, http.StatusFound))
	t.Cleanup(moved.Close)
	redirected := newMCPRegistry(&config.Config{
		MCPRegistryURL:     moved.URL,
		MCPPrivateNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	if _, err := redirected.Search(t.Context(), "x", 1); err == nil {
		t.Error("a redirect was followed")
	}
}
