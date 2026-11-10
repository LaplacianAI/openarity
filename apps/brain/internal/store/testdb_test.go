package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runID names everything one `go test` process creates, so two runs against
// the same server never drop each other's schemas or databases.
var runID = func() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

// A migrated database, built once per process, that every queryStore copies.
// Migrating per test took the package past eight minutes: Migrate takes an
// advisory lock with one fixed id for the whole database, so every test's
// migration queued behind the last. CREATE DATABASE ... TEMPLATE copies the
// migrated schema and its seed rows in about a third of the time, and takes
// no lock another test waits on.
var testDBs struct {
	once      sync.Once
	adminOnce sync.Once
	adminErr  error
	admin     *pgxpool.Pool
	template  string
	err       error
	seq       atomic.Int64
}

// withDatabase is dsn on the named database, or its own when name is empty.
func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	if name != "" {
		u.Path = "/" + name
	}
	// A test database is thrown away, so a commit need not wait for its
	// WAL to reach the disk: on a containerised Postgres that wait is most
	// of the suite. Session-scoped, so production is untouched, and a crash
	// can lose the last commits, never corrupt anything.
	q := u.Query()
	q.Set("synchronous_commit", "off")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// connectAdmin opens the one pool every test database is created and
// dropped through, on the database the DSN names.
func connectAdmin(dsn string) error {
	adminDSN, err := withDatabase(dsn, "")
	if err != nil {
		return err
	}
	admin, err := pgxpool.New(context.Background(), adminDSN)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	testDBs.admin = admin
	return nil
}

func buildTemplate(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	testDBs.adminOnce.Do(func() { testDBs.adminErr = connectAdmin(dsn) })
	if testDBs.adminErr != nil {
		return testDBs.adminErr
	}
	admin := testDBs.admin

	name := "brain_tmpl_" + runID
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	testDBs.template = name

	tdsn, err := withDatabase(dsn, name)
	if err != nil {
		return err
	}
	s, err := New(ctx, tdsn)
	if err != nil {
		return fmt.Errorf("connect to template: %w", err)
	}
	// Closed before any copy: CREATE DATABASE refuses a template anyone is
	// connected to.
	defer s.Close()
	if _, err := s.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return nil
}

// queryStore is a Store on a migrated database of the test's own, dropped
// when the test ends.
func queryStore(t *testing.T) *Store {
	t.Helper()

	dsn := liveDSN(t)
	testDBs.once.Do(func() { testDBs.err = buildTemplate(dsn) })
	if testDBs.err != nil {
		t.Fatalf("test database template: %v", testDBs.err)
	}
	return ownDatabase(t, dsn, "brain_t", " TEMPLATE "+testDBs.template)
}

// migrationStore is a Store on an empty database of the test's own, for the
// tests of migrating itself. A database rather than a schema: the migration
// lock is an advisory lock, which covers a whole database, so tests sharing
// one would wait on each other's lock, and so would two runs.
func migrationStore(t *testing.T) *Store {
	t.Helper()

	dsn := liveDSN(t)
	testDBs.adminOnce.Do(func() { testDBs.adminErr = connectAdmin(dsn) })
	if testDBs.adminErr != nil {
		t.Fatalf("connect: %v", testDBs.adminErr)
	}
	return ownDatabase(t, dsn, "brain_m", "")
}

func ownDatabase(t *testing.T, dsn, prefix, template string) *Store {
	t.Helper()

	name := fmt.Sprintf("%s_%s_%d", prefix, runID, testDBs.seq.Add(1))
	if _, err := testDBs.admin.Exec(t.Context(), "CREATE DATABASE "+name+template); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()
		// FORCE ends whatever the test left connected, so a leaked
		// connection fails nothing and leaves nothing behind.
		if _, err := testDBs.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	tdsn, err := withDatabase(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), tdsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// dropTemplate runs once the package's tests are done.
func dropTemplate() {
	if testDBs.admin == nil {
		return
	}
	defer testDBs.admin.Close()
	if testDBs.template == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = testDBs.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+testDBs.template+" WITH (FORCE)")
}
