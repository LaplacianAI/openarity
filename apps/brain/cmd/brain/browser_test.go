package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/browser"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
)

// The browser is dialed through the list MCP_PRIVATE_NETWORKS makes: a
// self-hosted one at an address not named in it is never reached.
func TestTheBrowserIsDialedOnlyWhereTheListAllows(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()

	c := browserClient(reachable(nil))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	resp, err := c.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "not an address the browser may reach") || reached {
		t.Fatalf("Do = %v, reached %v; want loopback refused", err, reached)
	}

	named := browserClient(reachable([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	resp, err = named.Do(req)
	if err != nil {
		t.Fatalf("a named address refused: %v", err)
	}
	_ = resp.Body.Close()
}

// A run holds the browser's connection open, so the client that dials it has
// no timeout, follows no redirect, and speaks HTTP/1.1: a websocket's
// upgrade is not HTTP/2's.
func TestTheBrowsersClientHoldsAConnectionOpen(t *testing.T) {
	c := browserClient(reachable(nil))
	if c.Timeout != 0 {
		t.Errorf("timeout %s, want none", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.ForceAttemptHTTP2 {
		t.Errorf("transport %+v, want HTTP/1.1 only", c.Transport)
	}
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) == nil {
		t.Error("a redirect would be followed")
	}
}

func TestTheBrowsersAreBuiltFromTheTeamsProvider(t *testing.T) {
	d := newBrowsers(static.New(), http.DefaultClient, reachable(nil), &fakeResolver{})
	if _, err := d.New(browser.Kernel, "", ""); err == nil || !strings.Contains(err.Error(), "needs a key") {
		t.Errorf("kernel with no key = %v", err)
	}
	if o, err := d.New(browser.Lightpanda, "ws://lightpanda:9222", ""); err != nil || o == nil {
		t.Errorf("lightpanda = %v, %v", o, err)
	}
	if d.Client == nil || d.Public == nil || d.Secrets == nil {
		t.Errorf("deps %+v, want each set", d)
	}
}

// brokenListener fails at once, as a listener whose socket is gone does.
type brokenListener struct{ net.Listener }

func (brokenListener) Accept() (net.Conn, error) { return nil, errors.New("the socket is gone") }

// A browser proxy that stops on its own stops the worker with it, and one
// stopped with the worker does not.
func TestABrowserProxyThatStopsStopsTheWorker(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	p := browser.NewProxy(func(netip.Addr) bool { return false }, slog.New(slog.DiscardHandler))

	ctx, stop := context.WithCancelCause(t.Context())
	defer stop(nil)
	serveBrowserProxy(ctx, p, brokenListener{l}, stop)
	if err := context.Cause(ctx); !errors.Is(err, errProxyStopped) {
		t.Errorf("after the proxy failed, the worker's cause = %v", err)
	}

	ctx, stop = context.WithCancelCause(t.Context())
	stop(context.Canceled)
	stopped := false
	serveBrowserProxy(ctx, p, l, func(error) { stopped = true })
	if stopped {
		t.Error("a proxy stopped with the worker stopped it again")
	}
}
