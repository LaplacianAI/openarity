package store

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3/lock"
)

// fs.Sub rejects a path that is not a valid io/fs path — anything absolute or
// containing "..". The error has to be wrapped and returned rather than
// ignored, because a nil *Provider handed to goose panics.
func TestProviderForRejectsAnInvalidDirectory(t *testing.T) {
	t.Parallel()

	s := &Store{}

	for _, dir := range []string{"../escape", "/absolute", ""} {
		p, err := s.providerFor(migrationFS, dir)
		if err == nil {
			t.Errorf("dir %q accepted", dir)
			continue
		}
		if p != nil {
			t.Errorf("dir %q returned a provider alongside the error", dir)
		}
		if !strings.Contains(err.Error(), "migrations") {
			t.Errorf("dir %q gave an error that does not say what failed: %v", dir, err)
		}
	}
}

// Renaming the migrations directory without updating the string here must
// fail, not silently apply nothing. "0 migrations applied" in a deploy log is
// indistinguishable from success.
//
// fs.Sub does not catch this on its own — a valid path that does not exist
// returns no error and an empty filesystem. goose does, at NewProvider, with
// "no migrations found". This test pins that, because the guarantee lives in
// goose rather than in our code and could change with an upgrade.
func TestProviderForRejectsAMissingDirectory(t *testing.T) {
	t.Parallel()

	s := &Store{}

	_, err := s.providerFor(migrationFS, "no-such-directory")
	if err == nil {
		t.Fatal("a missing migrations directory was accepted — a rename would silently apply nothing")
	}
	if !strings.Contains(err.Error(), "no migrations") {
		t.Errorf("error does not explain the directory is empty: %v", err)
	}
}

// The embedded migrations must actually be where the code says they are. This
// is a compile-time-ish check that survives a directory rename, and needs no
// database.
func TestMigrationsAreEmbedded(t *testing.T) {
	t.Parallel()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations embedded")
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			t.Errorf("%s is not a .sql file", e.Name())
		}
	}
}

// Every migration must define both directions. goose accepts a file with no
// Down and fails only when someone tries to roll back — at which point they
// are already having a bad day.
func TestEveryMigrationHasUpAndDown(t *testing.T) {
	t.Parallel()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	for _, e := range entries {
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}

		for _, want := range []string{"-- +goose Up", "-- +goose Down"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s has no %q section", e.Name(), want)
			}
		}
		if strings.Contains(string(body), "up SQL query") {
			t.Errorf("%s still contains the scaffold placeholder", e.Name())
		}
	}
}

// providerFor checks the error from lock.NewPostgresSessionLocker, and that
// branch is uncovered on purpose. The constructor fails only when an option's
// apply() returns an error, and goose's WithLockID accepts any int64
// unconditionally:
//
//	func WithLockID(lockID int64) SessionLockerOption {
//		return sessionLockerConfigFunc(func(c *sessionLockerConfig) error {
//			c.lockID = lockID
//			return nil
//		})
//	}
//
// Covering it means giving providerFor an options parameter that no caller
// ever passes, so that a test can supply a failing one — WithLockTimeout(0, 0)
// is the nearest candidate. A seam whose only user is a test is worse than an
// uncovered line.
//
// So pin the assumption instead, using the exact call the production code
// makes rather than an approximation of it. If a goose upgrade starts
// validating lock IDs, this fails and the branch becomes worth testing
// properly.
func TestTheSessionLockerWeBuildCannotFail(t *testing.T) {
	t.Parallel()

	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID))
	if err != nil {
		t.Fatalf("NewPostgresSessionLocker(WithLockID(%d)) now fails (%v) — "+
			"the error branch in providerFor is reachable and needs a real test",
			migrationLockID, err)
	}
	if locker == nil {
		t.Fatal("NewPostgresSessionLocker returned nil with no error")
	}
}

// Migrate and Rollback both check the error from provider(), and both branches
// are uncovered for the same reason one level up: provider() calls
// providerFor(migrationFS, "migrations"), and none of its three failure modes
// can fire on those arguments.
//
//   - fs.Sub only rejects an invalid path, and "migrations" is a constant
//   - the locker cannot fail, per the test above
//   - goose.NewProvider rejects an empty directory, and TestMigrationsAreEmbedded
//     proves it is not empty
//
// This pins the conclusion directly: if provider() ever starts failing, the
// two branches stop being dead and this test says so.
func TestProviderCannotFail(t *testing.T) {
	t.Parallel()

	p, err := (&Store{}).provider()
	if err != nil {
		t.Fatalf("provider() now fails (%v) — the error branches in Migrate "+
			"and Rollback are reachable and need real tests", err)
	}
	if p == nil {
		t.Fatal("provider() returned nil with no error")
	}
}

// fstest.MapFS stands in for a different embed. providerFor must work with any
// fs.FS, which is what makes the seam testable at all.
func TestProviderForAcceptsAnyFS(t *testing.T) {
	t.Parallel()

	s := &Store{}
	fsys := fstest.MapFS{
		"sql/00001_x.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}

	if _, err := s.providerFor(fsys, "sql"); err != nil {
		t.Errorf("providerFor rejected a valid alternative filesystem: %v", err)
	}
}
