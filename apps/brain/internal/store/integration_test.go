package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// liveDSN returns a DSN for a real Postgres, or skips. CI sets
// BRAIN_TEST_POSTGRES_DSN from its service container; locally, export it
// against whatever compose brings up. Skipping rather than failing keeps
// `make test` useful with nothing running.
func liveDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

// The whole point of Ping: against a database that is actually there, it must
// succeed. Every other test in this package asserts the failure direction, so
// without this one a Ping that always errored would still pass the suite.
func TestPingSucceedsAgainstRealPostgres(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping against a live database: %v", err)
	}
}

// The pool must survive more concurrent callers than it has connections.
// MaxConns is 10; anything past that queues rather than erroring.
func TestPoolQueuesBeyondMaxConns(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	errs := make(chan error, maxConns*3)
	for range maxConns * 3 {
		go func() { errs <- s.Ping(ctx) }()
	}
	for range maxConns * 3 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Ping failed: %v", err)
		}
	}
}
