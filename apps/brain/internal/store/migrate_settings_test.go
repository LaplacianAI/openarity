package store

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

// Migrations run on the store's own pool, so a setting one of them makes for
// the session outlives it on a connection the brain then serves queries from.
// A lock_timeout left behind turned an ordinary wait for a row lock into an
// error three seconds in. Every connection the pool hands out after migrating
// has the server's defaults.
func TestMigratingLeavesNoSettingOnThePool(t *testing.T) {
	s, err := New(t.Context(), storetest.Empty(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	conns := make([]*pgxpool.Conn, 0, maxConns)
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	for range maxConns {
		c, err := s.pool.Acquire(t.Context())
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		conns = append(conns, c)
		var timeout string
		if err := c.QueryRow(t.Context(), `SHOW lock_timeout`).Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != "0" {
			t.Errorf("a pooled connection has lock_timeout %s after migrating, want 0", timeout)
		}
	}
}
