package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

func httpClientOf(t *testing.T, api *client.ClientWithResponses) *http.Client {
	t.Helper()

	raw, ok := api.ClientInterface.(*client.Client)
	if !ok {
		t.Fatalf("the generated client is a %T", api.ClientInterface)
	}
	hc, ok := raw.Client.(*http.Client)
	if !ok {
		t.Fatalf("the HTTP client is a %T", raw.Client)
	}
	return hc
}

// A run's events are read for as long as the run lasts. The ordinary client's
// 30-second timeout covers reading the body too, so following any run longer
// than that would be cut off mid-stream with a timeout nobody can explain.
// Only the wait for the headers may be bounded.
func TestTheStreamingClientHasNoOverallTimeout(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	opts := optionsFor(t, server.URL, &fakeStore{}, "")
	opts.TokenFlag = "a-token"

	stream, err := opts.Stream(t.Context())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	hc := httpClientOf(t, stream)
	if hc.Timeout != 0 {
		t.Errorf("the streaming client gives up after %s", hc.Timeout)
	}
	transport, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the streaming transport is a %T", hc.Transport)
	}
	if transport.ResponseHeaderTimeout != requestTimeout {
		t.Errorf("a brain that never answers is waited on for %s", transport.ResponseHeaderTimeout)
	}

	api, err := opts.API(t.Context())
	if err != nil {
		t.Fatalf("API: %v", err)
	}
	if got := httpClientOf(t, api).Timeout; got != requestTimeout {
		t.Errorf("the ordinary client's timeout is %s, want %s", got, requestTimeout)
	}
}

// The stream is a second way to reach the brain, and it must carry the same
// credential as the first — a stream that sent none would answer 401 to a
// person who is logged in.
func TestTheStreamingClientSendsTheCredential(t *testing.T) {
	got := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	opts := optionsFor(t, server.URL, &fakeStore{}, "")
	opts.TokenFlag = "a-token"

	stream, err := opts.Stream(t.Context())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	res, err := stream.GetHealth(t.Context())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = res.Body.Close()

	if auth := <-got; auth != "Bearer a-token" {
		t.Errorf("Authorization = %q, want the token", auth)
	}
}
