package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

// The directory being a parameter is what makes every test here parallel: no
// HOME to fake, no XDG variable that macOS ignores anyway.
func newStore(t *testing.T) *FileStore {
	t.Helper()
	// A directory that does not exist yet, so write() has to create it. Using
	// t.TempDir() directly would assert nothing about MkdirAll.
	return NewFileStore(filepath.Join(t.TempDir(), "openarity"))
}

func aLogin() credential.Credential {
	return credential.Credential{
		Token:   "an-access-token",
		Refresh: "a-refresh-token",
		Expiry:  time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC),
	}
}

func TestACredentialSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	want := aLogin()

	if err := s.Set("staging", want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("staging")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != want.Token || got.Refresh != want.Refresh {
		t.Errorf("Credential = %+v, want %+v", got, want)
	}
	// A refresh token that survives but an expiry that does not is a silent
	// re-login an hour later, and nothing before then would show it.
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

// The whole reason this file is separate from config.yaml. A credential file
// readable by every process on a shared box is the thing the split was for.
func TestTheFileIsOnlyReadableByItsOwner(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	info, err := os.Stat(s.Location())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}

	dir, err := os.Stat(filepath.Dir(s.Location()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", perm)
	}
}

// A fresh install has no file. Reporting that as an error would make every
// command fail before anyone had a chance to log in.
func TestAMissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	got, err := newStore(t).Get("local")
	if err != nil {
		t.Fatalf("Get with no file: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("Get returned %+v from a file that does not exist", got)
	}
}

// Reached whenever no context is selected — a fresh install on the way to the
// default server. It must not be an error, and must not be a lookup either.
func TestAnEmptyContextReadsAsNothing(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("")
	if err != nil {
		t.Fatalf("Get(\"\"): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("an empty context name answered with %+v", got)
	}
}

// Writing under an empty name puts a credential somewhere nothing can read it
// back, which looks exactly like the login having failed.
func TestACredentialCannotBeStoredWithoutAContext(t *testing.T) {
	t.Parallel()

	if err := newStore(t).Set("", aLogin()); err == nil {
		t.Fatal("a credential was stored under an empty context name")
	}
}

// `oa logout` twice is not an error, and neither is logging out of a context
// that only ever used --token.
func TestDeletingWhatIsNotThereSucceeds(t *testing.T) {
	t.Parallel()

	s := newStore(t)

	if err := s.Delete("never-logged-in"); err != nil {
		t.Errorf("Delete on an empty store: %v", err)
	}
	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete("local"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("local"); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

// The point of keying by context: logging out of one must not touch another.
func TestDeletingOneContextLeavesTheOthers(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	for _, name := range []string{"local", "staging", "prod"} {
		if err := s.Set(name, credential.Credential{Token: name + "-token"}); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}

	if err := s.Delete("staging"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for name, want := range map[string]string{
		"local":   "local-token",
		"staging": "",
		"prod":    "prod-token",
	} {
		got, err := s.Get(name)
		if err != nil {
			t.Fatalf("Get %s: %v", name, err)
		}
		if got.Token != want {
			t.Errorf("%s: token = %q, want %q", name, got.Token, want)
		}
	}
}

func TestRenameMovesTheCredential(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Set("old", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Rename("old", "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	moved, _ := s.Get("new")
	if moved.Token != aLogin().Token {
		t.Errorf("the credential did not arrive under the new name: %+v", moved)
	}
	left, _ := s.Get("old")
	if !left.IsZero() {
		t.Errorf("the credential is still readable under the old name: %+v", left)
	}
}

// Write-then-delete on the same key deletes what was just written. A rename
// that changes nothing must not be the one operation that loses a login.
func TestRenamingToTheSameNameKeepsTheCredential(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Rename("local", "local"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, _ := s.Get("local")
	if got.IsZero() {
		t.Error("renaming a context to its own name deleted its credential")
	}
}

// Unreachable through `oa context rename`, which refuses an existing name
// first. Reaching it means config.yaml and this file disagree, and the
// credential that would be overwritten is the one in use.
func TestRenameRefusesToOverwriteACredential(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Set("from", credential.Credential{Token: "from-token"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("to", credential.Credential{Token: "to-token"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Rename("from", "to"); err == nil {
		t.Fatal("a rename overwrote an existing credential")
	}

	kept, _ := s.Get("to")
	if kept.Token != "to-token" {
		t.Errorf("the existing credential was damaged: %+v", kept)
	}
}

// A context with no credential is normal — it may only ever have used --token.
// Renaming it must not invent one or fail.
func TestRenamingAContextWithNoCredentialIsANoOp(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := s.Rename("never-logged-in", "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, _ := s.Get("new")
	if !got.IsZero() {
		t.Errorf("a credential appeared from nowhere: %+v", got)
	}
}

// yaml.Unmarshal fills nothing for an empty document and leaves the map nil,
// which panics on the first assignment. An interrupted write or a hand-emptied
// file both produce exactly this.
func TestAnEmptyFileIsWritableRatherThanAPanic(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Location()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.Location(), []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set onto an empty file: %v", err)
	}
	got, _ := s.Get("local")
	if got.IsZero() {
		t.Error("the credential was not stored")
	}
}

// Silently starting from scratch would throw away every other context's login
// because one line got mangled.
func TestACorruptFileIsReportedRatherThanIgnored(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Location()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.Location(), []byte("credentials: [this is not a map\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := s.Get("local")
	if err == nil {
		t.Fatal("a corrupt credentials file was read as empty")
	}
	if !strings.Contains(err.Error(), s.Location()) {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// The temp file is written into the same directory so the rename is atomic.
// One left behind is a credential sitting in a file nothing will ever clean up.
func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	for _, name := range []string{"local", "staging"} {
		if err := s.Set(name, aLogin()); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(s.Location()))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != FileName {
			t.Errorf("%s was left in the config directory", entry.Name())
		}
	}
}

// It is what `oa config show` reports as the token's source, so it has to be
// the actual file and not the directory it sits in.
func TestLocationNamesTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if got := NewFileStore(dir).Location(); got != filepath.Join(dir, FileName) {
		t.Errorf("Location() = %q", got)
	}
}
