package store

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

// No test in this file may be parallel. MockInit swaps a package-level
// provider inside go-keyring, so two tests running at once would be looking at
// each other's keychain.
//
// It also means these tests cannot see the one limit that matters: the mock
// accepts a 16KB secret where macOS refuses past about 3009 bytes and Windows
// past 2560. ErrTooBig is therefore unreachable from here — the fallback that
// handles it is tested in store_test.go with a fake that can produce it.
func mockKeychain(t *testing.T) *KeyringStore {
	t.Helper()

	keyring.MockInit()
	t.Cleanup(keyring.MockInit) // leave a clean one behind for whatever runs next

	s, err := NewKeyringStore()
	if err != nil {
		t.Fatalf("NewKeyringStore: %v", err)
	}
	return s
}

func TestTheKeychainRoundTripsACredential(t *testing.T) {
	s := mockKeychain(t)
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
	// The keychain stores one string per account, so the whole credential is
	// marshalled into it. An expiry lost here is a login that cannot tell it
	// has died.
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

// ErrNotFound is how an empty keychain answers, and the file store answers the
// same question with a zero value. The two have to agree or the fallback in
// store.go cannot tell "nothing here" from "something broke".
func TestAnAbsentCredentialIsNotAnError(t *testing.T) {
	s := mockKeychain(t)

	got, err := s.Get("never-logged-in")
	if err != nil {
		t.Fatalf("Get on an empty keychain: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("Get returned %+v from an empty keychain", got)
	}
}

// Measured, not assumed: the keychain returns ErrNotFound for deleting
// something absent where the file store returns success. Success is right —
// `oa logout` twice is not an error — so this swallow has to stay.
func TestDeletingWhatIsNotInTheKeychainSucceeds(t *testing.T) {
	s := mockKeychain(t)

	if err := s.Delete("never-logged-in"); err != nil {
		t.Errorf("Delete on an empty keychain: %v", err)
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

	got, _ := s.Get("local")
	if !got.IsZero() {
		t.Errorf("the credential outlived its deletion: %+v", got)
	}
}

// The keychain accepts an empty account name silently, which would file the
// credential somewhere nothing reads back. The guard has to be ours.
func TestTheKeychainRefusesAnEmptyContext(t *testing.T) {
	s := mockKeychain(t)

	if err := s.Set("", aLogin()); err == nil {
		t.Error("a credential was stored under an empty account name")
	}
	got, err := s.Get("")
	if err != nil {
		t.Errorf("Get(\"\"): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("an empty context answered with %+v", got)
	}
}

// Two contexts against one brain as different people is the reason the account
// is the context name. Writing one must not disturb the other.
func TestEachContextIsItsOwnKeychainEntry(t *testing.T) {
	s := mockKeychain(t)

	for _, name := range []string{"staging-admin", "staging-member"} {
		if err := s.Set(name, credential.Credential{Token: name + "-token"}); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}

	if err := s.Delete("staging-admin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	member, _ := s.Get("staging-member")
	if member.Token != "staging-member-token" {
		t.Errorf("the other context's credential = %+v", member)
	}
}

func TestKeychainRenameMovesTheCredential(t *testing.T) {
	s := mockKeychain(t)
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
		t.Errorf("the credential is still under the old name: %+v", left)
	}
}

// Rename here is Set-then-Delete against the same account, so the same name
// twice would store it and immediately remove it.
func TestKeychainRenamingToTheSameNameKeepsTheCredential(t *testing.T) {
	s := mockKeychain(t)
	if err := s.Set("local", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Rename("local", "local"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got, _ := s.Get("local"); got.IsZero() {
		t.Error("renaming a context to its own name deleted its credential")
	}
}

func TestKeychainRenameRefusesToOverwrite(t *testing.T) {
	s := mockKeychain(t)
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

// The whole reason NewKeyringStore probes rather than assuming: an SSH
// session, a container and CI have no keychain, and `oa` has to notice before
// it relies on one rather than failing a login later.
func TestNoKeychainIsReportedByTheConstructor(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	t.Cleanup(keyring.MockInit)

	if _, err := NewKeyringStore(); err == nil {
		t.Fatal("a machine with no usable keychain produced a KeyringStore")
	}
}

// It reaches a person through `oa config show`, so it has to name something
// they could go and look in.
func TestLocationNamesSomethingAPersonCanFind(t *testing.T) {
	s := mockKeychain(t)

	got := s.Location()
	if got == "" {
		t.Fatal("Location() is empty")
	}
	if strings.Contains(got, "/") {
		t.Errorf("Location() = %q — a keychain is not a path", got)
	}
}
