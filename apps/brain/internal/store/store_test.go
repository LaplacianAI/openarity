package store

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// deadAddr returns an address nothing is listening on, so a connection
// attempt is refused immediately rather than timing out.
func deadAddr(t *testing.T) string {
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

// deadDSN is syntactically valid and points at nothing.
func deadDSN(t *testing.T) string {
	t.Helper()
	return "postgres://user:hunter2@" + deadAddr(t) + "/openarity?sslmode=disable"
}

// New must reject a DSN it cannot parse, and must not hand back a Store
// alongside the error.
func TestNewRejectsAnUnparseableDSN(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"postgres://user:pw@localhost:5432/db?sslmode=nonsense",
		"://bad:pw@host",
		"not a dsn at all",
		// Pool settings ride in the DSN and are validated by ParseConfig, not
		// at first use. A nonsense pool size must stop the process starting.
		"postgres://user:pw@localhost:5432/db?sslmode=disable&pool_max_conns=0",
	} {
		s, err := New(t.Context(), dsn)
		if err == nil {
			t.Errorf("%q accepted", dsn)
			continue
		}
		if s != nil {
			t.Errorf("%q returned a Store alongside the error", dsn)
		}
	}
}

// The DSN carries the password. pgx redacts it inside its own error, but the
// wrapping must not add the raw string back.
func TestNewDoesNotLeakThePassword(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), "postgres://user:hunter2@localhost:5432/db?sslmode=nonsense")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaked the password: %v", err)
	}
}

// pgxpool is lazy: NewWithConfig dials nothing. A stopped database, a wrong
// host and a wrong password all return a working *Pool and a nil error.
// Ping is the only thing that proves the database is reachable, which is why
// run must call it before the listeners come up.
func TestNewDoesNotConnect(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), deadDSN(t))
	if err != nil {
		t.Fatalf("New failed on an unreachable database, so it is dialling: %v", err)
	}
	t.Cleanup(s.Close)
}

// Ping against a database that is not there must fail, and fail quickly.
func TestPingFailsWhenPostgresIsDown(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), deadDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := s.Ping(ctx); err == nil {
		t.Error("Ping succeeded against an address nothing is listening on")
	}
}

// Ping must honour its context rather than blocking on the pool's own
// timeouts. A health check that outlives its deadline stalls the probe.
func TestPingHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), deadDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- s.Ping(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Ping succeeded with a cancelled context")
		}
	case <-time.After(3 * time.Second):
		t.Error("Ping ignored its cancelled context")
	}
}

// Close runs on the shutdown path, where a panic would mask the real error.
func TestCloseIsSafeToCallTwice(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), deadDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Close panicked: %v", r)
		}
	}()

	s.Close()
	s.Close()
}

// A Ping after Close must return an error, not panic. Shutdown races the
// health check: the probe can arrive while the pool is closing.
func TestPingAfterCloseDoesNotPanic(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), deadDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Close()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Ping after Close panicked: %v", r)
		}
	}()

	if err := s.Ping(t.Context()); err == nil {
		t.Error("Ping succeeded on a closed pool")
	}
}
