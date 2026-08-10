package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// liveDSN returns a DSN for a real Postgres, or skips. CI sets
// BRAIN_TEST_POSTGRES_DSN from its service container.
func liveDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

// waitForHealthz blocks until the listener answers 200, or fails the test.
func waitForHealthz(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
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
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never answered /healthz", addr)
}

// schemaDSN points a copy of the live DSN at a schema of its own, created for
// this test and dropped afterwards. Migration tests must not touch whatever
// else lives in the test database.
func schemaDSN(t *testing.T) string {
	t.Helper()

	base := liveDSN(t)
	schema := "brain_cmd_" + strings.ToLower(t.Name())

	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(t.Context())) }()

	drop := "DROP SCHEMA IF EXISTS " + schema + " CASCADE"
	if _, err := admin.Exec(t.Context(), drop); err != nil {
		t.Fatalf("clear schema: %v", err)
	}
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()

		if _, err := conn.Exec(ctx, drop); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	return u.String()
}

// migrate up, then up again, then down — driven through run exactly as the
// deploy Job drives it. Nothing else covers migrate, migrateUp or migrateDown.
func TestMigrateCommandEndToEnd(t *testing.T) {
	t.Setenv("OPENARITY_POSTGRES_DSN", schemaDSN(t))

	var first bytes.Buffer
	if err := run(t.Context(), &first, []string{"migrate", "up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if !strings.Contains(first.String(), `"count":1`) && !strings.Contains(first.String(), "count=1") {
		t.Errorf("migrate up did not report applying one migration: %s", first.String())
	}

	// Re-running is what a redeploy does. It must be a no-op, not an error.
	var second bytes.Buffer
	if err := run(t.Context(), &second, []string{"migrate", "up"}); err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
	if !strings.Contains(second.String(), `"count":0`) && !strings.Contains(second.String(), "count=0") {
		t.Errorf("second migrate up did not report zero applied: %s", second.String())
	}

	var down bytes.Buffer
	if err := run(t.Context(), &down, []string{"migrate", "down"}); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}

// migrate must not start a listener. A Job that binds a port would collide
// with the running Deployment and hold the port for the life of the Job.
func TestMigrateCommandStartsNoListener(t *testing.T) {
	t.Setenv("OPENARITY_POSTGRES_DSN", schemaDSN(t))

	apiBind := freeAddr(t)
	t.Setenv("OPENARITY_API_BIND", apiBind)
	t.Setenv("OPENARITY_WEBHOOK_BIND", freeAddr(t))

	var buf bytes.Buffer
	if err := run(t.Context(), &buf, []string{"migrate", "up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	if strings.Contains(buf.String(), "Listening") {
		t.Errorf("migrate started a listener: %s", buf.String())
	}

	// The port must still be free once migrate has returned.
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", apiBind)
	if err != nil {
		t.Fatalf("migrate left %s bound: %v", apiBind, err)
	}
	_ = l.Close()
}

// The one path the unit tests cannot reach: Postgres is up, Ping succeeds, the
// listeners come up, and a cancelled context brings the whole thing down
// cleanly. Everything after the Ping in run is covered only by this test.
func TestRunServesWithARealDatabase(t *testing.T) {
	dsn := liveDSN(t)

	apiBind := freeAddr(t)
	t.Setenv("OPENARITY_API_BIND", apiBind)
	t.Setenv("OPENARITY_WEBHOOK_BIND", freeAddr(t))
	t.Setenv("OPENARITY_POSTGRES_DSN", dsn)

	// serve refuses to start with no way to authenticate anyone, so the
	// development token stands in for an identity provider here.
	t.Setenv("OPENARITY_DEV_TOKEN", "integration-token")

	ctx, cancel := context.WithCancel(t.Context())

	var buf syncBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, &buf, nil) }()

	waitForHealthz(t, apiBind)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run after a clean shutdown = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run never returned after the context was cancelled")
	}

	out := buf.String()
	for _, want := range []string{"Starting brain service", "Listening", apiBind} {
		if !strings.Contains(out, want) {
			t.Errorf("log never mentioned %q: %s", want, out)
		}
	}
}

// A brain that can authenticate nobody would bind both listeners, pass its
// probes, and reject every real request. run must refuse before anything
// listens — and it must say which variable to set.
func TestRunRefusesToServeWithNoWayToAuthenticate(t *testing.T) {
	dsn := liveDSN(t)

	apiBind := freeAddr(t)
	t.Setenv("OPENARITY_API_BIND", apiBind)
	t.Setenv("OPENARITY_WEBHOOK_BIND", freeAddr(t))
	t.Setenv("OPENARITY_POSTGRES_DSN", dsn)
	t.Setenv("OPENARITY_DEV_TOKEN", "")
	t.Setenv("OPENARITY_OIDC_ENABLED", "false")

	var buf syncBuffer
	err := run(t.Context(), &buf, nil)
	if err == nil {
		t.Fatal("run served with nothing able to authenticate")
	}
	if !strings.Contains(err.Error(), "OPENARITY_DEV_TOKEN") {
		t.Errorf("error does not say how to fix it: %v", err)
	}

	// Nothing may be left bound: a half-started process holds the port and the
	// next attempt fails for the wrong reason.
	var lc net.ListenConfig
	l, listenErr := lc.Listen(t.Context(), "tcp", apiBind)
	if listenErr != nil {
		t.Fatalf("run left %s bound after refusing to start: %v", apiBind, listenErr)
	}
	_ = l.Close()
}

// The logger writes from the goroutine running run while the test reads after
// it returns. bytes.Buffer is not safe for that on its own, and -race says so.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
