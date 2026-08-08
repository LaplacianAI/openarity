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
// branch is uncovered on purpose: the constructor only fails when an option's
// apply() fails, and we pass no options, so the loop it lives in never runs.
//
// Covering it would mean adding a lock.SessionLockerOption parameter that no
// caller ever passes. Instead, pin the assumption here. If a goose upgrade
// makes the constructor fallible with no options, this fails and the branch
// becomes worth testing properly.
func TestSessionLockerCannotFailWithoutOptions(t *testing.T) {
	t.Parallel()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		t.Fatalf("NewPostgresSessionLocker now fails with no options (%v) — "+
			"the error branch in providerFor is reachable and needs a test", err)
	}
	if locker == nil {
		t.Fatal("NewPostgresSessionLocker returned nil with no error")
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
