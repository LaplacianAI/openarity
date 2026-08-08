package store

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// migrationStore builds a Store pinned to a schema of its own, so migration
// tests neither collide with each other nor leave anything behind. Every
// table the migrations create — and goose's own version table — lands inside
// that schema and is dropped with it.
func migrationStore(t *testing.T) *Store {
	t.Helper()

	dsn := liveDSN(t)
	schema := "brain_test_" + strings.ToLower(t.Name())

	admin, err := New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()

	drop := "DROP SCHEMA IF EXISTS " + schema + " CASCADE"
	if _, err := admin.pool.Exec(t.Context(), drop); err != nil {
		t.Fatalf("clear schema: %v", err)
	}
	if _, err := admin.pool.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	t.Cleanup(func() {
		cleanup, err := New(context.WithoutCancel(t.Context()), dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer cleanup.Close()

		if _, err := cleanup.pool.Exec(context.WithoutCancel(t.Context()), drop); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := New(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(s.Close)

	return s
}

// tableExists respects search_path, so it answers for this test's schema only.
func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()

	var exists bool
	if err := s.pool.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return exists
}

// The base case: migrations apply to an empty database and produce the schema
// they describe.
func TestMigrateAppliesFromEmpty(t *testing.T) {
	s := migrationStore(t)

	if tableExists(t, s, "teams") {
		t.Fatal("teams already exists before migrating")
	}

	applied, err := s.Migrate(t.Context())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if applied == 0 {
		t.Error("Migrate applied nothing — check that fs.Sub strips the migrations/ prefix")
	}
	if !tableExists(t, s, "teams") {
		t.Error("teams does not exist after migrating")
	}
}

// Deploys re-run migrations constantly. The second run must be a no-op, not an
// error and not a re-apply.
func TestMigrateIsIdempotent(t *testing.T) {
	s := migrationStore(t)

	first, err := s.Migrate(t.Context())
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	second, err := s.Migrate(t.Context())
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if second != 0 {
		t.Errorf("second Migrate applied %d migrations, want 0 (first applied %d)", second, first)
	}
}

// A Down that does not actually reverse its Up only shows up on the second Up,
// which fails with "already exists". Nothing but this exact sequence catches it.
func TestMigrateDownThenUpAgain(t *testing.T) {
	s := migrationStore(t)

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := s.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if tableExists(t, s, "teams") {
		t.Error("teams survived the rollback, so Down does not reverse Up")
	}

	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after Rollback: %v", err)
	}
	if !tableExists(t, s, "teams") {
		t.Error("teams missing after re-applying")
	}
}

// Two migrators can overlap: a Job pod on an unreachable node is replaced
// while the original is still running. Without WithSessionLocker both apply
// the same migrations and collide.
//
// Simply racing two goroutines does not test this — the migration finishes
// before the second one reads the version table, and the test passes with the
// locker removed. So hold goose's advisory lock from the test and prove
// Migrate waits for it.
func TestMigrateWaitsForTheSessionLock(t *testing.T) {
	s := migrationStore(t)

	holder, err := s.pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer holder.Release()

	// Session-scoped, so it stays held until this exact connection releases it.
	if _, err := holder.Exec(t.Context(), "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatalf("take the migration lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { _, err := s.Migrate(t.Context()); done <- err }()

	select {
	case err := <-done:
		t.Fatalf("Migrate ran while the lock was held (err=%v) — WithSessionLocker is not wired", err)
	case <-time.After(500 * time.Millisecond):
		// Correct: still blocked.
	}

	if _, err := holder.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
		t.Fatalf("release the migration lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Migrate after the lock was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Migrate never proceeded after the lock was released")
	}

	if !tableExists(t, s, "teams") {
		t.Error("teams missing after the lock was released")
	}
}
