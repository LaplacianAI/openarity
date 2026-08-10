package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freeAddr returns an address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

// occupiedAddr returns an address held for the lifetime of the test, so
// binding it fails.
func occupiedAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// waitForHealthz blocks until the listener answers 200, or fails the test.
func waitForHealthz(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never answered /healthz", addr)
}

// waitFor returns Run's error, or fails if Run never returns.
func waitFor(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("Run never returned")
		return nil
	}
}

// Both listeners must actually serve, and a cancelled context must stop them
// cleanly. ListenAndServe returns http.ErrServerClosed on shutdown — mapping
// that to nil is the difference between a rolling update and a crash loop.
func TestRunServesBothThenStopsCleanly(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = freeAddr(t)

	ctx, cancel := context.WithCancel(t.Context())
	srv := New(cfg, discardLogger(), healthyDB(), testVerifier())

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	waitForHealthz(t, cfg.APIBind)
	waitForHealthz(t, cfg.WebhookBind)

	cancel()
	if err := waitFor(t, done); err != nil {
		t.Errorf("Run after a clean shutdown = %v, want nil", err)
	}
}

// A listener that cannot bind must bring the process down, not leave it
// serving half its surface. Silent webhook loss looks like a healthy pod.
func TestRunFailsFastWhenAPortIsTaken(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = occupiedAddr(t)

	done := make(chan error, 1)
	go func() { done <- New(cfg, discardLogger(), healthyDB(), testVerifier()).Run(t.Context()) }()

	if err := waitFor(t, done); err == nil {
		t.Error("Run returned nil despite the webhook listener failing to bind")
	}
}

// The failing listener must take the healthy one with it. Assert the API port
// was actually released rather than trusting Run's return value.
func TestRunStopsTheHealthyListenerToo(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = occupiedAddr(t)

	done := make(chan error, 1)
	go func() { done <- New(cfg, discardLogger(), healthyDB(), testVerifier()).Run(t.Context()) }()
	_ = waitFor(t, done)

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", cfg.APIBind)
	if err != nil {
		t.Fatalf("api listener still holding %s after a webhook failure: %v", cfg.APIBind, err)
	}
	_ = l.Close()
}

// Run must survive a context that is already cancelled — the shutdown path is
// reached before either listener is up.
func TestRunWithAlreadyCancelledContext(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = freeAddr(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- New(cfg, discardLogger(), healthyDB(), testVerifier()).Run(ctx) }()

	if err := waitFor(t, done); err != nil {
		t.Errorf("Run with a cancelled context = %v, want nil", err)
	}
}

// The startup log has to name the address each listener is on. It is the only
// record of where the process actually bound, and the first thing anyone reads
// when a port turns out to be wrong.
func TestRunLogsBothBindAddresses(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = freeAddr(t)

	logger, buf := recordingLogger()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- New(cfg, logger, healthyDB(), testVerifier()).Run(ctx) }()
	if err := waitFor(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := buf.String()
	for _, addr := range []string{cfg.APIBind, cfg.WebhookBind} {
		if !strings.Contains(out, addr) {
			t.Errorf("startup log never mentioned %s: %s", addr, out)
		}
	}
}

// Every record must be one JSON object per line. A handler misconfigured to
// emit text produces logs no aggregator can query, and nothing else notices.
func TestRunLogsAreLineDelimitedJSON(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.APIBind = freeAddr(t)
	cfg.WebhookBind = freeAddr(t)

	logger, buf := recordingLogger()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- New(cfg, logger, healthyDB(), testVerifier()).Run(ctx) }()
	if err := waitFor(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a record per listener plus shutdown, got %d: %s", len(lines), buf.String())
	}
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("log line is not JSON: %q", line)
			continue
		}
		if rec["msg"] == nil {
			t.Errorf("log line has no msg: %q", line)
		}
	}
}

// shutdown on a server that never started is a no-op, not an error.
func TestShutdownBeforeRun(t *testing.T) {
	t.Parallel()

	if err := New(testConfig(), discardLogger(), healthyDB(), testVerifier()).shutdown(); err != nil {
		t.Errorf("shutdown before Run = %v, want nil", err)
	}
}

// listen turns a clean close into success. Called directly so the mapping is
// covered even if Run's plumbing changes.
func TestListenMapsServerClosedToNil(t *testing.T) {
	t.Parallel()

	s := newHTTPServer(freeAddr(t), New(testConfig(), discardLogger(), healthyDB(), testVerifier()).apiHandler())

	done := make(chan error, 1)
	go func() { done <- listen(s) }()

	waitForHealthz(t, s.Addr)

	ctx, cancel := context.WithTimeout(t.Context(), shutdownTimeout)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if err := waitFor(t, done); err != nil {
		t.Errorf("listen after a clean Shutdown = %v, want nil", err)
	}
}

// listen must pass a real failure through untouched.
func TestListenReturnsBindError(t *testing.T) {
	t.Parallel()

	if err := listen(newHTTPServer(occupiedAddr(t), New(testConfig(), discardLogger(), healthyDB(), testVerifier()).apiHandler())); err == nil {
		t.Error("listen returned nil for an address already in use")
	}
}
