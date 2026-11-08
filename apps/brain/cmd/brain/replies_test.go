package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func post(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

// A reply URL that names a host resolving to a private address passed the
// API's check, which reads literals only. The dial is what refuses it.
func TestAReplyIsNotPostedToAnAddressTheTeamMayNotReach(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the reply reached a loopback server")
	}))
	t.Cleanup(srv.Close)

	url := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	resp, err := post(t, replyClient(reachable(nil)), url)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the reply was posted to a loopback address")
	}
	if !strings.Contains(err.Error(), "not an address a reply may reach") {
		t.Errorf("err = %v, want the dial refused", err)
	}
}

// The operator's private networks are reachable, as they are for web search.
func TestAReplyMayReachTheNetworksTheOperatorNamed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	resp, err := post(t, replyClient(reachable([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})), srv.URL)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// A redirect is the answer. Following it would carry the reply somewhere the
// URL check never saw, and a 302 turns the POST into a GET.
func TestAReplyFollowsNoRedirect(t *testing.T) {
	t.Parallel()

	reached := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	t.Cleanup(elsewhere.Close)
	redirect := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)

	c := replyClient(reachable([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}))
	resp, err := post(t, c, redirect.URL)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if reached || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status %d, reached the target %v; want the 307 itself", resp.StatusCode, reached)
	}
	if c.Timeout != replyTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, replyTimeout)
	}
}
