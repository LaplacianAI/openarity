package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

const dexConfig = `issuer: http://127.0.0.1:5556
storage:
  type: sqlite3
  config:
    file: /an/install/dex/dex.db
staticClients:
  - id: openarity
    redirectURIs:
      - http://127.0.0.1:21120/auth/callback
enablePasswordDB: true
staticPasswords:
  - email: dev@openarity.local
    username: dev
    userID: 0d1e9f3c-6a52-4f5d-8b71-2c4e6a8d0f13
    hash: "$2a$10$oldoldoldoldoldoldoldoldoldoldoldoldoldoldoldoldoldoldo"
`

func writeConfig(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(dexConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The passphrase is shown once and kept only as a bcrypt hash, so losing it
// used to mean deleting the install — the database, the identity and every
// connection — to get a new password.
func TestResettingReplacesTheHashWithOneThatMatches(t *testing.T) {
	t.Parallel()

	path := writeConfig(t)

	passphrase, err := ResetPassphrase(path, "")
	if err != nil {
		t.Fatalf("ResetPassphrase() = %v", err)
	}
	if len(passphrase) != passphraseLength {
		t.Errorf("passphrase is %d characters, want %d", len(passphrase), passphraseLength)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The property that matters: the hash in the file is the hash of the
	// passphrase the person was just shown. A reset that writes a hash of
	// something else locks them out for good.
	hash := between(t, string(written), `hash: "`, `"`)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(passphrase)); err != nil {
		t.Errorf("the hash written does not match the passphrase returned: %v", err)
	}
	if strings.Contains(string(written), "oldoldold") {
		t.Error("the old hash is still there")
	}
}

// Everything else in somebody's dex configuration is theirs. A reset that
// reformatted it, reordered it or dropped a field would be a surprise nobody
// asked for.
func TestResettingChangesNothingElse(t *testing.T) {
	t.Parallel()

	path := writeConfig(t)
	if _, err := ResetPassphrase(path, "chosen-by-hand"); err != nil {
		t.Fatalf("ResetPassphrase() = %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	before := strings.Split(dexConfig, "\n")
	after := strings.Split(string(written), "\n")
	if len(before) != len(after) {
		t.Fatalf("the file went from %d lines to %d", len(before), len(after))
	}
	for i := range before {
		if strings.Contains(before[i], "hash:") {
			continue
		}
		if before[i] != after[i] {
			t.Errorf("line %d changed:\n  before %q\n  after  %q", i+1, before[i], after[i])
		}
	}
}

// A passphrase somebody chose is the one that is hashed. Generating one
// anyway and showing it would look like it worked and would not.
func TestAChosenPassphraseIsTheOneThatIsSet(t *testing.T) {
	t.Parallel()

	path := writeConfig(t)

	got, err := ResetPassphrase(path, "the-one-they-typed")
	if err != nil {
		t.Fatalf("ResetPassphrase() = %v", err)
	}
	if got != "the-one-they-typed" {
		t.Errorf("ResetPassphrase() = %q, want the passphrase it was given", got)
	}

	written, _ := os.ReadFile(path)
	hash := between(t, string(written), `hash: "`, `"`)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("the-one-they-typed")); err != nil {
		t.Errorf("the hash does not match what was chosen: %v", err)
	}
}

// A file this did not write is not one to rewrite blind.
func TestSomethingThatIsNotADexConfigurationIsRefused(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("issuer: http://127.0.0.1:5556\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ResetPassphrase(path, ""); err == nil {
		t.Error("ResetPassphrase() on a file with no hash = nil, want it to refuse")
	}
}

func between(t *testing.T, s, open, close string) string {
	t.Helper()

	start := strings.Index(s, open)
	if start < 0 {
		t.Fatalf("no %q in:\n%s", open, s)
	}
	start += len(open)
	end := strings.Index(s[start:], close)
	if end < 0 {
		t.Fatalf("no closing %q in:\n%s", close, s)
	}
	return s[start : start+end]
}
