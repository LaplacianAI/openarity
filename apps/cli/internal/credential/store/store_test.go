package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/LaplacianAI/openarity/apps/cli/internal/credential"
)

// fakeStore exists because the two real stores cannot produce the conditions
// the fallback is written for. The keychain mock accepts any size, so
// ErrTooBig is unreachable through it, and neither store can be made to fail
// on demand.
type fakeStore struct {
	name      string
	creds     map[string]credential.Credential
	setErr    error
	deleteErr error
	renameErr error
}

func newFake(name string) *fakeStore {
	return &fakeStore{name: name, creds: map[string]credential.Credential{}}
}

func (f *fakeStore) Location() string { return f.name }

func (f *fakeStore) Get(context string) (credential.Credential, error) {
	return f.creds[context], nil
}

func (f *fakeStore) Set(context string, cred credential.Credential) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.creds[context] = cred
	return nil
}

func (f *fakeStore) Delete(context string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.creds, context)
	return nil
}

func (f *fakeStore) Rename(from, to string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	moving, ok := f.creds[from]
	if !ok || from == to {
		return nil
	}
	f.creds[to] = moving
	delete(f.creds, from)
	return nil
}

func newFallback() (*fallback, *fakeStore, *fakeStore) {
	keychain, file := newFake("keychain"), newFake("file")
	return &fallback{preferred: keychain, file: file}, keychain, file
}

// The SSH case. A credential written on a machine that had no keychain is
// still yours the next time you sit at it — without the read-through, the same
// machine forgets your login depending on how you arrived.
func TestAReadFallsThroughToTheFile(t *testing.T) {
	t.Parallel()

	f, _, file := newFallback()
	file.creds["local"] = credential.Credential{Token: "written-over-ssh"}

	got, err := f.Get("local")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != "written-over-ssh" {
		t.Errorf("Credential = %+v — the file was not consulted", got)
	}
}

// If both hold one, the keychain's is the newer: writes go there whenever it
// works, so a file copy can only be older.
func TestTheKeychainWinsWhenBothHaveOne(t *testing.T) {
	t.Parallel()

	f, keychain, file := newFallback()
	keychain.creds["local"] = credential.Credential{Token: "current"}
	file.creds["local"] = credential.Credential{Token: "stale"}

	got, _ := f.Get("local")
	if got.Token != "current" {
		t.Errorf("Credential = %+v, want the keychain's", got)
	}
}

// The resurrection bug, and the reason Set touches both stores. Log in on a
// machine with no keychain, log in again later on the same machine with one,
// then arrive over SSH: without this the read falls through to a credential
// from before the second login and hands back a token that is weeks old.
func TestAWriteToTheKeychainClearsAnyOlderCopyInTheFile(t *testing.T) {
	t.Parallel()

	f, keychain, file := newFallback()
	file.creds["local"] = credential.Credential{Token: "from-an-ssh-session"}

	if err := f.Set("local", credential.Credential{Token: "fresh"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, ok := file.creds["local"]; ok {
		t.Errorf("the old copy is still in the file: %+v", file.creds["local"])
	}
	if keychain.creds["local"].Token != "fresh" {
		t.Errorf("the keychain holds %+v", keychain.creds["local"])
	}
}

// A credential too large for the keychain must still log the person in — they
// have already opened a browser and clicked approve, and throwing that away
// over a size limit is the worst possible moment to fail.
//
// The keychain starts with an older credential deliberately. Someone whose
// token outgrew the limit had a smaller one there yesterday, and that is the
// only arrangement where the clean-up matters: reads prefer the keychain, so
// an entry left behind shadows the new file copy for good. An empty keychain
// asserts nothing, because the clean-up has nothing to remove.
func TestACredentialTooLargeForTheKeychainGoesToTheFile(t *testing.T) {
	t.Parallel()

	f, keychain, file := newFallback()
	keychain.creds["local"] = credential.Credential{Token: "yesterdays-smaller-token"}
	keychain.setErr = ErrTooBig

	if err := f.Set("local", credential.Credential{Token: "a-very-long-jwt"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if file.creds["local"].Token != "a-very-long-jwt" {
		t.Errorf("the file holds %+v", file.creds["local"])
	}
	if _, ok := keychain.creds["local"]; ok {
		t.Errorf("the older credential was left in the keychain: %+v", keychain.creds["local"])
	}

	// The consequence, not just the mechanism: a read has to answer with the
	// new token rather than the one the keychain was still holding.
	got, _ := f.Get("local")
	if got.Token != "a-very-long-jwt" {
		t.Errorf("Get returned %+v — the stale keychain entry won", got)
	}
}

// Only ErrTooBig is a routing decision. A keychain that is locked, or refuses
// for any other reason, is a real failure and must not silently downgrade the
// credential to a file the person did not ask for.
func TestAnyOtherKeychainFailureIsNotSilentlyDowngraded(t *testing.T) {
	t.Parallel()

	f, keychain, file := newFallback()
	broken := errors.New("the keychain is locked")
	keychain.setErr = broken

	err := f.Set("local", credential.Credential{Token: "t"})
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the keychain's own failure", err)
	}
	if _, ok := file.creds["local"]; ok {
		t.Error("the credential was written to the file anyway")
	}
}

// A logout that leaves a copy behind is not a logout: the read-through finds
// the survivor and logs the person straight back in.
func TestALogoutClearsBothStores(t *testing.T) {
	t.Parallel()

	f, keychain, file := newFallback()
	keychain.creds["local"] = credential.Credential{Token: "in-the-keychain"}
	file.creds["local"] = credential.Credential{Token: "in-the-file"}

	if err := f.Delete("local"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := f.Get("local")
	if !got.IsZero() {
		t.Errorf("still logged in after a logout: %+v", got)
	}
}

// Which store holds it depends on how the login happened, and a rename cannot
// know. Asking both is cheaper than tracking it.
func TestRenameReachesWhicheverStoreHasIt(t *testing.T) {
	t.Parallel()

	for _, where := range []string{"keychain", "file"} {
		t.Run(where, func(t *testing.T) {
			t.Parallel()

			f, keychain, file := newFallback()
			holder := keychain
			if where == "file" {
				holder = file
			}
			holder.creds["old"] = credential.Credential{Token: "t"}

			if err := f.Rename("old", "new"); err != nil {
				t.Fatalf("Rename: %v", err)
			}

			moved, _ := f.Get("new")
			if moved.Token != "t" {
				t.Errorf("the credential did not move: %+v", moved)
			}
			left, _ := f.Get("old")
			if !left.IsZero() {
				t.Errorf("it is still readable under the old name: %+v", left)
			}
		})
	}
}

// Not parallel: MockInit swaps a package-level provider inside go-keyring.
//
// A machine with no keychain still has to log in. Returning an error here
// would make `oa login` impossible in CI, in a container, and over SSH.
func TestOpenFallsBackToTheFileWithNoKeychain(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	t.Cleanup(keyring.MockInit)

	dir := t.TempDir()
	got := Open(dir)

	if _, ok := got.(*FileStore); !ok {
		t.Fatalf("Open returned %T, want the file store", got)
	}
	if got.Location() != filepath.Join(dir, FileName) {
		t.Errorf("Location() = %q", got.Location())
	}
}

// Not parallel, same reason.
func TestOpenPrefersTheKeychainWhenThereIsOne(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	got := Open(t.TempDir())

	chain, ok := got.(*fallback)
	if !ok {
		t.Fatalf("Open returned %T, want the fallback pair", got)
	}
	if _, ok := chain.preferred.(*KeyringStore); !ok {
		t.Errorf("preferred is %T, want the keychain", chain.preferred)
	}
	if _, ok := chain.file.(*FileStore); !ok {
		t.Errorf("the fallback is %T, want the file store", chain.file)
	}
}

// The end-to-end shape of the thing, through Open rather than a hand-built
// pair: log in, and the credential comes back.
func TestOpenProducesAWorkingStore(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)

	s := Open(t.TempDir())
	if err := s.Set("staging", aLogin()); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("staging")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != aLogin().Token {
		t.Errorf("Credential = %+v", got)
	}

	if err := s.Delete("staging"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if after, _ := s.Get("staging"); !after.IsZero() {
		t.Errorf("still there after a logout: %+v", after)
	}
}

// The location a caller is shown is the keychain's, because that is where a
// credential of ordinary size actually goes. Reporting the file would make
// `oa config show` name a path that usually holds nothing, and renewal
// compares this string against the source Resolve picked — get it wrong and
// nothing is ever renewed.
func TestTheFallbackReportsThePreferredLocation(t *testing.T) {
	t.Parallel()

	both, keychain, _ := newFallback()

	if got := both.Location(); got != keychain.Location() {
		t.Errorf("Location() = %q, want the keychain's %q", got, keychain.Location())
	}
}

// Logging out has to clear both, and a failure in the first must not leave the
// second untouched and unreported — the credential would survive in a store
// nothing mentions and the next command would still be logged in.
func TestAFailedDeleteIsReportedRatherThanHalfDone(t *testing.T) {
	t.Parallel()

	locked := errors.New("the keychain is locked")
	both, keychain, file := newFallback()
	keychain.creds["local"] = credential.Credential{Token: "a"}
	file.creds["local"] = credential.Credential{Token: "b"}
	keychain.deleteErr = locked

	if err := both.Delete("local"); !errors.Is(err, locked) {
		t.Fatalf("Delete = %v, want the underlying failure", err)
	}
	if _, ok := file.creds["local"]; !ok {
		t.Error("the file copy was removed even though the first delete failed")
	}
}

func TestAFailedRenameIsReported(t *testing.T) {
	t.Parallel()

	locked := errors.New("the keychain is locked")
	both, keychain, file := newFallback()
	keychain.creds["old"] = credential.Credential{Token: "a"}
	file.creds["old"] = credential.Credential{Token: "b"}
	keychain.renameErr = locked

	if err := both.Rename("old", "new"); !errors.Is(err, locked) {
		t.Fatalf("Rename = %v, want the underlying failure", err)
	}
	if _, ok := file.creds["new"]; ok {
		t.Error("the file copy was renamed even though the first rename failed")
	}
}

// The oversized path writes to the file and then clears the keychain. If that
// clear fails the caller has to hear about it: reads go keychain-first, so a
// stale entry left there would be served ahead of the credential just written.
func TestAnOversizedWriteReportsAFailedKeychainClear(t *testing.T) {
	t.Parallel()

	locked := errors.New("the keychain is locked")
	both, keychain, file := newFallback()
	keychain.setErr = ErrTooBig
	keychain.deleteErr = locked

	err := both.Set("local", credential.Credential{Token: "a-very-large-token"})
	if !errors.Is(err, locked) {
		t.Fatalf("Set = %v, want the failed clear reported", err)
	}
	if _, ok := file.creds["local"]; !ok {
		t.Error("the credential never reached the file")
	}
}

// A write that fails for any other reason is that failure, not a fallback.
// Falling back on every error would quietly move credentials out of the
// keychain the first time it was locked.
func TestOnlyAnOversizedWriteFallsBackToTheFile(t *testing.T) {
	t.Parallel()

	locked := errors.New("the keychain is locked")
	both, keychain, file := newFallback()
	keychain.setErr = locked

	if err := both.Set("local", credential.Credential{Token: "a"}); !errors.Is(err, locked) {
		t.Fatalf("Set = %v, want the underlying failure", err)
	}
	if _, ok := file.creds["local"]; ok {
		t.Error("a locked keychain silently moved the credential to the file")
	}
}
