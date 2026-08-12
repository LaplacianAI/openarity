package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Every test here writes a real file, so each gets its own config directory.
// os.UserConfigDir reads the environment, which is why these cannot be
// t.Parallel(): t.Setenv and parallel subtests are mutually exclusive.
func isolate(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // macOS ignores XDG_CONFIG_HOME
	return dir
}

func TestLoadReturnsNothingWhenThereIsNoFile(t *testing.T) {
	isolate(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with no file: %v", err)
	}
	if cfg != (Config{}) {
		t.Errorf("Load = %+v, want the zero value", cfg)
	}
}

// The CLI has to work before anything has been saved. Treating a missing file
// as an error would mean a fresh install could not run a single command.
func TestSaveThenLoadRoundTrips(t *testing.T) {
	isolate(t)

	want := Config{Server: "https://brain.example.com", Token: "a-token"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// The file holds a bearer token. Anything group- or world-readable is a
// credential handed to every other account on the machine.
func TestTheSavedFileIsOwnerOnly(t *testing.T) {
	isolate(t)

	if err := Save(Config{Server: "https://brain.example.com", Token: "a-token"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is %o, want 600", path, perm)
	}

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Dir(path), err)
	}
	if perm := dir.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("%s is %o, want no group or other bits", filepath.Dir(path), perm)
	}
}

// A second save must not leave a half-written credential behind. Writing to a
// temporary file and renaming is what makes the replacement atomic.
func TestSaveReplacesRatherThanAppends(t *testing.T) {
	isolate(t)

	if err := Save(Config{Server: "https://one.example.com", Token: "first"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := Save(Config{Server: "https://two.example.com", Token: "second"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Token != "second" || got.Server != "https://two.example.com" {
		t.Errorf("Load = %+v, want only the second save", got)
	}
}

// Nothing may be left in the config directory but the file itself. A
// temporary file that survived would hold the same token under a name nobody
// cleans up.
func TestSaveLeavesNoTemporaryFile(t *testing.T) {
	isolate(t)

	if err := Save(Config{Server: "https://brain.example.com", Token: "a-token"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Dir(path), err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the config directory holds %v, want only the config file", names)
	}
}

// A corrupt file must say which file and why. Returning the zero value
// instead would silently drop a saved token and send the user through a login
// they already did.
func TestLoadRejectsAFileItCannotParse(t *testing.T) {
	isolate(t)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("server: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a file that is not YAML")
	} else if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// An empty token is absent, not a token. Writing `token: ""` into the file
// and reading it back as a credential sends an empty bearer header, which is
// a 401 that looks like the login failed rather than never happened.
func TestAnEmptyTokenIsNotWrittenAsAValue(t *testing.T) {
	isolate(t)

	if err := Save(Config{Server: "https://brain.example.com"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "token:") {
		t.Errorf("the file carries an empty token key: %q", data)
	}
}

func TestPathIsUnderTheUserConfigDirectory(t *testing.T) {
	isolate(t)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("Path() = %s, want it to end in config.yaml", path)
	}
	if filepath.Base(filepath.Dir(path)) != "openarity" {
		t.Errorf("Path() = %s, want it under an openarity directory", path)
	}
}

// The reason Save writes through a temporary file and renames it, rather than
// writing to the path directly. os.WriteFile opens with O_TRUNC, so a reader
// arriving mid-write sees an empty or half-written file — and Load parses an
// empty file happily, returning a config with no token. The user is then told
// to log in again despite having just logged in.
//
// A rename is atomic within a filesystem: a reader sees the old file or the
// new one, never a partial one. This test is what makes that claim real —
// swap Save's body for os.WriteFile and it fails.
func TestSaveIsAtomicUnderConcurrentReads(t *testing.T) {
	isolate(t)

	const token = "a-token-that-must-never-appear-truncated"
	saved := Config{Server: "https://brain.example.com", Token: token}

	if err := Save(saved); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	var readers, writer sync.WaitGroup
	stop := make(chan struct{})
	failures := make(chan string, 64)

	// One writer, rewriting the same content as fast as it can.
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := Save(saved); err != nil {
					select {
					case failures <- "Save: " + err.Error():
					default:
					}
					return
				}
			}
		}
	}()

	// Readers, which must never observe anything but the whole file.
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 400 {
				got, err := Load()
				switch {
				case err != nil:
					select {
					case failures <- "Load saw a partial file: " + err.Error():
					default:
					}
					return
				case got.Token != token:
					select {
					case failures <- fmt.Sprintf("Load saw a truncated file: %+v", got):
					default:
					}
					return
				}
			}
		}()
	}

	// Readers first, then stop the writer. Closing stop before they run would
	// leave the writer finished and the test racing nothing.
	readers.Wait()
	close(stop)
	writer.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
}
