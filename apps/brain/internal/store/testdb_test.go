package store

import (
	"context"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

// migrateDSN brings a database up to the newest migration, for
// storetest.Migrated, and closes what it opened.
func migrateDSN(ctx context.Context, dsn string) error {
	s, err := New(ctx, dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.Migrate(ctx)
	return err
}

// queryStore is a Store on a migrated database of the test's own.
func queryStore(t *testing.T) *Store {
	t.Helper()
	return open(t, storetest.Migrated(t, migrateDSN))
}

// migrationStore is a Store on an empty database of the test's own, for the
// tests of migrating itself. A database rather than a schema: the migration
// lock covers a whole database, so tests sharing one would wait on each
// other's lock, and so would two runs.
func migrationStore(t *testing.T) *Store {
	t.Helper()
	return open(t, storetest.Empty(t))
}

func open(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}
