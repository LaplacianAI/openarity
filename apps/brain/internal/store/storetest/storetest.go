// Package storetest gives a test a Postgres database of its own: a copy of
// one migrated once per test process, or an empty one. Only tests import it.
//
// Tests once migrated a schema each, in one shared database. Migrating takes
// an advisory lock with a fixed id, and an advisory lock covers a whole
// database, so every test's migration queued behind the last: the brain's
// check took eight to ten minutes. CREATE DATABASE ... TEMPLATE copies a
// migrated database in about 0.2 s and takes no lock another test waits on,
// so tests holding one can run in parallel.
//
// It imports nothing from the store, so the store's own tests can use it; the
// caller says how to migrate.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const timeout = 2 * time.Minute

// runID names everything one process creates, so two runs against one server
// never drop each other's databases.
var runID = func() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

var state struct {
	adminOnce sync.Once
	admin     *pgxpool.Pool
	adminErr  error

	templateOnce sync.Once
	template     string
	templateErr  error

	seq atomic.Int64
}

// DSN is BRAIN_TEST_POSTGRES_DSN, or the test is skipped.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

// Migrated is the DSN of a database of the test's own, a copy of one that
// migrate brought up once in this process. It is dropped when the test ends.
func Migrated(t testing.TB, migrate func(ctx context.Context, dsn string) error) string {
	t.Helper()
	base := DSN(t)
	admin(t, base)
	state.templateOnce.Do(func() { state.templateErr = buildTemplate(base, migrate) })
	if state.templateErr != nil {
		t.Fatalf("the migrated template: %v", state.templateErr)
	}
	return create(t, base, "brain_t", " TEMPLATE "+state.template)
}

// Empty is the DSN of an empty database of the test's own, for a test of
// migrating itself. It is dropped when the test ends.
func Empty(t testing.TB) string {
	t.Helper()
	base := DSN(t)
	admin(t, base)
	return create(t, base, "brain_e", "")
}

// Main runs a package's tests, then drops the template they copied.
func Main(m *testing.M) int {
	code := m.Run()
	if state.admin != nil {
		if state.template != "" {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			_, _ = state.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+state.template+" WITH (FORCE)")
			cancel()
		}
		state.admin.Close()
	}
	return code
}

// WithDatabase is dsn on the named database. A test database is thrown away,
// so a commit need not wait for its WAL to reach the disk; on a containerised
// Postgres that wait was most of the suite. It is set per session, so nothing
// outside the tests changes, and a crash can lose the last commits, never
// corrupt anything.
func WithDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	if name != "" {
		u.Path = "/" + name
	}
	q := u.Query()
	q.Set("synchronous_commit", "off")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func admin(t testing.TB, base string) {
	t.Helper()
	state.adminOnce.Do(func() {
		dsn, err := WithDatabase(base, "")
		if err != nil {
			state.adminErr = err
			return
		}
		state.admin, state.adminErr = pgxpool.New(context.Background(), dsn)
	})
	if state.adminErr != nil {
		t.Fatalf("connect: %v", state.adminErr)
	}
}

func buildTemplate(base string, migrate func(ctx context.Context, dsn string) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	name := "brain_tmpl_" + runID
	if _, err := state.admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	state.template = name

	dsn, err := WithDatabase(base, name)
	if err != nil {
		return err
	}
	// migrate must close what it opened: CREATE DATABASE refuses a template
	// anyone is connected to.
	return migrate(ctx, dsn)
}

func create(t testing.TB, base, prefix, template string) string {
	t.Helper()

	name := fmt.Sprintf("%s_%s_%d", prefix, runID, state.seq.Add(1))
	if _, err := state.admin.Exec(t.Context(), "CREATE DATABASE "+name+template); err != nil {
		t.Fatalf("create a test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), timeout)
		defer cancel()
		// FORCE ends what the test left connected, so a leaked connection
		// fails nothing and leaves nothing behind.
		if _, err := state.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the test database: %v", err)
		}
	})

	dsn, err := WithDatabase(base, name)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}
