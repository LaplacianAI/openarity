package main

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/websearch"
)

// newBrowsers is a team's browser: a session provider's API on sessions, and
// the browser itself dialed by a client of its own. Both reach what
// MCP_PRIVATE_NETWORKS allows, so a self-hosted browser is named there; the
// pages it opens reach only public hosts, whatever the list allows.
func newBrowsers(secretStore secrets.Store, sessions *http.Client, allow func(netip.Addr) bool, r resolver) *runtime.BrowserDeps {
	return &runtime.BrowserDeps{
		Secrets: secretStore,
		New: func(p browser.Provider, url, key string) (browser.Opener, error) {
			return browser.New(p, url, key, sessions)
		},
		Client: browserClient(allow),
		Public: browser.Public(publicHost(r)),
	}
}

// browserClient dials the browser's websocket. No timeout: a run holds the
// connection open, and the dial has its own. HTTP/1.1 only, as a websocket's
// upgrade is not HTTP/2's: a transport with a dialer of its own tries
// HTTP/2 only when forced to.
func browserClient(allow func(netip.Addr) bool) *http.Client {
	t := websearch.Transport(allow, "the browser")
	t.ForceAttemptHTTP2 = false
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("a browser may not redirect")
		},
	}
}
