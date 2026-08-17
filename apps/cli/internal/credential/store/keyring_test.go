package store

import (
	"errors"
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

// `oa context rename` renames the context whether or not anyone has logged
// into it, so Rename is reached with nothing to move. Treating that as an
// error would make renaming a context you have not logged into fail, and
// treating it as something to move would file a zero credential under the new
// name — which reads back as a login that is present but empty.
func TestKeychainRenamingAContextWithNoCredentialIsNotAnError(t *testing.T) {
	s := mockKeychain(t)

	if err := s.Rename("never-logged-in", "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Get cannot tell the difference: a stored zero credential unmarshals to
	// the same value as nothing stored at all. The assertion has to be that
	// no keychain entry exists, or carrying the empty credential through
	// would pass this test while filing an empty secret under every context
	// name anybody ever renamed.
	if _, err := keyring.Get(service, "renamed"); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("the rename created a keychain entry for a context with no login (err = %v)", err)
	}
}

// An empty destination is the same hazard Set guards against, reached one
// call further in: the keychain accepts an empty account name, so the
// credential would move somewhere nothing reads back — a silent logout.
//
// Measured: deleting Rename's own guard does not change the outcome here.
// Rename reaches Set, Set refuses with the same sentence, and the source
// survives because the refusal lands before the Delete. The guard is
// defence-in-depth, and a mutation sweep will report it as surviving forever.
// The file store's equivalent guard is not redundant — FileStore.Rename
// writes the map directly rather than going through Set, so removing it there
// really does file a credential under an empty key.
func TestKeychainRenameRefusesAnEmptyDestination(t *testing.T) {
	s := mockKeychain(t)
	if err := s.Set("prod", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Rename("prod", ""); err == nil {
		t.Fatal("a credential was renamed to an empty context")
	}

	kept, _ := s.Get("prod")
	if kept.Token != aLogin().Token {
		t.Errorf("the source credential was lost: %+v", kept)
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

// ErrTooBig is the hinge of the whole fallback: store.Set checks
// errors.Is(err, ErrTooBig) to decide between "write to the file instead" and
// "fail the login". If this translation stops happening, an oversized token
// becomes a hard failure on exactly the machines that needed the fallback —
// and nothing else would notice, because the keyring mock accepts any size
// and never produces ErrSetDataTooBig itself.
func TestAnOversizedSecretBecomesErrTooBig(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrSetDataTooBig)
	t.Cleanup(keyring.MockInit)

	s := &KeyringStore{}
	err := s.Set("local", credential.Credential{Token: "a-token-larger-than-the-keychain-allows"})

	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("Set = %v, want it to satisfy errors.Is(err, ErrTooBig)", err)
	}
	// The size is what makes the message actionable — "too large" alone leaves
	// somebody guessing whether it was by ten bytes or ten kilobytes.
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("the error does not say how large it was: %v", err)
	}
}

// Any other keychain failure is that failure, not a fallback signal. Treating
// a locked keychain as ErrTooBig would silently move credentials out of it,
// which is the opposite of what a locked keychain means.
func TestAnotherKeychainFailureIsNotErrTooBig(t *testing.T) {
	locked := errors.New("the keychain is locked")
	keyring.MockInitWithError(locked)
	t.Cleanup(keyring.MockInit)

	s := &KeyringStore{}
	err := s.Set("local", credential.Credential{Token: "a-token"})

	if err == nil {
		t.Fatal("a locked keychain accepted a write")
	}
	if errors.Is(err, ErrTooBig) {
		t.Errorf("a locked keychain was reported as an oversized credential: %v", err)
	}
	if !errors.Is(err, locked) {
		t.Errorf("the underlying failure was lost: %v", err)
	}
}

// Rename must not overwrite a context that already holds a login. The two
// credentials belong to different brains, and silently replacing one is a
// logout nobody asked for and cannot undo.
func TestRenameRefusesToOverwriteAnExistingCredential(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	s := &KeyringStore{}
	if err := s.Set("old", credential.Credential{Token: "the-one-being-moved"}); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := s.Set("new", credential.Credential{Token: "the-one-already-there"}); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	if err := s.Rename("old", "new"); err == nil {
		t.Fatal("a rename onto an occupied context succeeded")
	}

	for name, want := range map[string]string{
		"old": "the-one-being-moved",
		"new": "the-one-already-there",
	} {
		got, err := s.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if got.Token != want {
			t.Errorf("%s = %q, want %q — a refused rename must change nothing", name, got.Token, want)
		}
	}
}

// The order inside Rename — write the new entry, then remove the old — is
// deliberate: an interruption between the two duplicates a credential rather
// than losing it, and losing it means a silent logout with no way back.
//
// It is not reachable through go-keyring's mock, and that is worth stating
// rather than leaving as an untested line. MockInitWithError fails *every*
// operation, so a failure injected for the write also fails the Get before it
// and Rename returns before reaching the part under test; MockInit installs a
// fresh empty provider, so a working keychain cannot be restored afterwards to
// inspect what survived. Reaching it would mean injecting the three keyring
// calls as package-level variables, which is more indirection in production
// code than one ordering is worth.
//
// This test pins the assumption instead: if the mock ever gains per-operation
// failures, it starts failing and the real assertion becomes writable.
func TestTheKeyringMockCannotFailOneOperationAtATime(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrSetDataTooBig)
	t.Cleanup(keyring.MockInit)

	if _, err := keyring.Get(service, "anything"); err == nil {
		t.Error("the mock now fails writes without failing reads — Rename's " +
			"write-before-delete order can be tested directly")
	}
}
