package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// readFile has two implementations and this is what holds both to the same
// contract. The Windows one opens the file by hand to allow a rename while it
// is being read, and getting that wrong would either break every config read
// or, more quietly, report a fresh install as a failure.
func TestReadFileReadsWhatWasWritten(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	want := []byte("contexts:\n  local:\n    server: https://brain.example.com\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	got, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile() = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("readFile() = %q, want %q", got, want)
	}
}

// A fresh install has no config file. Load turns this into an empty config, so
// a readFile that reported it as some other kind of error would make every
// command fail before anyone had a chance to run `oa context create`.
func TestReadFileReportsAMissingFileAsNotExist(t *testing.T) {
	t.Parallel()

	_, err := readFile(filepath.Join(t.TempDir(), "was-never-written.yaml"))
	if err == nil {
		t.Fatal("readFile() of a missing file = nil, want an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("readFile() = %v, want it to satisfy errors.Is(err, fs.ErrNotExist)", err)
	}
}

// An empty file is a config with nothing in it, not a failure. It is what a
// half-finished write used to leave behind, and what a person creates by
// touching the file to see where it goes.
func TestReadFileAcceptsAnEmptyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	got, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile() = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("readFile() = %q, want nothing", got)
	}
}
