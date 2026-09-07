package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteCreatesTheFileAndItsDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	want := []byte("contexts:\n  local:\n    server: https://brain.example.com\n")

	if err := Write(path, want); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("read back %q, want %q", got, want)
	}
}

// The file this replaces is read far more often than it is written, and yaml
// parses a truncated file happily into a zero value — so a torn read is not an
// error a person sees, it is a config that appears never to have been created.
func TestWriteReplacesRatherThanTruncates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Write(path, []byte("first")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if err := Write(path, []byte("second")); err != nil {
		t.Fatalf("Write() a second time = %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if string(got) != "second" {
		t.Errorf("Read() = %q, want only the second write", got)
	}
}

// Nothing is left behind to be mistaken for the real file, or to accumulate a
// directory of half-written configs over a year of use.
func TestWriteLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Write(path, []byte("written")); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v, want only config.yaml", names)
	}
}

// A leftover from an interrupted write should say which write it was. This is
// the only thing that distinguishes .config-1234.yaml from .stack-1234.yaml
// in a directory that holds both.
func TestTheTemporaryFileIsNamedAfterItsDestination(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{
		"/tmp/config.yaml":      ".config-*.yaml",
		"/tmp/credentials.yaml": ".credentials-*.yaml",
		"/tmp/stack.yaml":       ".stack-*.yaml",
		"/tmp/noextension":      ".noextension-*",
	} {
		if got := tempPattern(path); got != want {
			t.Errorf("tempPattern(%q) = %q, want %q", path, got, want)
		}
	}
}

// The destination directory, never the system temporary one: a rename across
// filesystems is not atomic, and on Windows not permitted at all.
func TestTheTemporaryFileIsMadeBesideItsDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// A directory where the destination should be. CreateTemp still succeeds,
	// so the failure arrives at the rename and names a path in dir.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("making %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatalf("filling %s: %v", path, err)
	}

	err := Write(path, []byte("written"))
	if err == nil {
		t.Fatal("Write() over a non-empty directory = nil, want an error")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Write() = %q, want it to name %s", err, dir)
	}
}

// A rename that cannot ever succeed has to report that, not spin. Only Windows
// exercises the waiting itself — a rename over an open file succeeds first time
// everywhere else — so this is what stops the loop being unbounded on every
// platform.
func TestReplaceGivesUpOnARenameThatCannotSucceed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "was-never-written")

	start := time.Now()
	err := replace(missing, filepath.Join(dir, "destination"))
	if err == nil {
		t.Fatal("replace() of a file that does not exist = nil, want an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("replace() took %s to give up, want it bounded", elapsed)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "destination")); statErr == nil {
		t.Error("replace() created the destination from nothing")
	}
}
