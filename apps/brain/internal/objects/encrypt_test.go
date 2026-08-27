package objects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// A store held in the test rather than internal/objects/inmemory, which
// imports this package — reaching back for it here would be an import cycle.
type fakeStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string][]byte{}} }

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	body, ok := f.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return append([]byte(nil), body...), nil
}

func (f *fakeStore) Put(_ context.Context, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.data[key] = append([]byte(nil), body...)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.data, key)
	return nil
}

// raw reads what actually landed, bypassing decryption. This is how the tests
// look inside the bucket.
func (f *fakeStore) raw(t *testing.T, key string) []byte {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	body, ok := f.data[key]
	if !ok {
		t.Fatalf("nothing stored at %s", key)
	}
	return append([]byte(nil), body...)
}

// A store with no Put, for the constructor's refusal.
type readOnlyStore struct{}

func (readOnlyStore) Get(context.Context, string) ([]byte, error) {
	return nil, ErrNotFound
}

// oneKey hands the same key to every team and counts how often it was asked,
// which is how the tests assert that a read of a missing object does not mint
// a key for a team that has none.
type oneKey struct {
	key   []byte
	err   error
	calls atomic.Int64
}

func newOneKey(fill byte) *oneKey {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = fill
	}
	return &oneKey{key: key}
}

func (k *oneKey) TeamKey(context.Context, uuid.UUID) ([]byte, error) {
	k.calls.Add(1)
	if k.err != nil {
		return nil, k.err
	}
	return k.key, nil
}

// perTeamKeys derives a distinct key per team, which is what production does.
type perTeamKeys struct{}

func (perTeamKeys) TeamKey(_ context.Context, teamID uuid.UUID) ([]byte, error) {
	key := make([]byte, KeySize)
	copy(key, teamID[:])
	copy(key[len(teamID):], teamID[:])
	return key, nil
}

func newEncrypted(t *testing.T, inner Store, keys KeySource) *Encrypted {
	t.Helper()

	e, err := NewEncrypted(inner, keys)
	if err != nil {
		t.Fatalf("NewEncrypted: %v", err)
	}
	return e
}

func objectKey(teamID uuid.UUID, name string) string {
	return TeamPrefix(teamID) + "objects/" + name
}

func TestEncryptedRoundTrips(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "abc")

	if err := e.Put(t.Context(), team, key, []byte("secret file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := e.Get(t.Context(), team, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "secret file" {
		t.Errorf("Get = %q, want the plaintext back", got)
	}
}

// The whole point. What lands in the object store must not contain the file.
func TestTheStoreNeverSeesPlaintext(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "abc")

	if err := e.Put(t.Context(), team, key, []byte("secret file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stored := inner.raw(t, key)
	if bytes.Contains(stored, []byte("secret file")) {
		t.Error("plaintext reached the object store")
	}

	// And it is the right length: 12 bytes of nonce and 16 of tag around the
	// file, which is also what stops this passing against a store that wrote
	// nothing at all.
	if want := 12 + len("secret file") + 16; len(stored) != want {
		t.Errorf("stored %d bytes, want %d — nonce + ciphertext + tag", len(stored), want)
	}
}

// GCM is authenticated. A flipped bit must fail, not decrypt to garbage: the
// ciphertext is the file XORed with a keystream, so without the tag an
// attacker who cannot read a file can still edit it blind.
func TestTamperedCiphertextFailsToDecrypt(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	key := objectKey(team, "abc")

	for name, corrupt := range map[string]func([]byte) []byte{
		"a flipped bit in the tag": func(b []byte) []byte {
			b[len(b)-1] ^= 0x01
			return b
		},
		"a flipped bit in the ciphertext": func(b []byte) []byte {
			b[13] ^= 0x01
			return b
		},
		"a flipped bit in the nonce": func(b []byte) []byte {
			b[0] ^= 0x01
			return b
		},
		"truncated": func(b []byte) []byte {
			return b[:len(b)-1]
		},
		// Shorter than a nonce is the one that would panic on a slice
		// expression rather than returning an error.
		"shorter than a nonce": func([]byte) []byte {
			return []byte("tiny")
		},
		"empty": func([]byte) []byte {
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inner := newFakeStore()
			e := newEncrypted(t, inner, newOneKey(0x11))

			if err := e.Put(t.Context(), team, key, []byte("secret file")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := inner.Put(t.Context(), key, corrupt(inner.raw(t, key))); err != nil {
				t.Fatalf("corrupting: %v", err)
			}

			got, err := e.Get(t.Context(), team, key)
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("err = %v, want ErrCorrupt", err)
			}
			if got != nil {
				t.Errorf("Get returned %d bytes alongside the error, want none", len(got))
			}
		})
	}
}

// Encrypting the same bytes twice must not produce the same ciphertext, or the
// bucket reveals which objects are identical — including across teams, where
// it would say "these two teams hold the same file".
func TestNonceIsFreshPerWrite(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	first, second := objectKey(team, "1"), objectKey(team, "2")

	for _, key := range []string{first, second} {
		if err := e.Put(t.Context(), team, key, []byte("same")); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
	}

	if bytes.Equal(inner.raw(t, first), inner.raw(t, second)) {
		t.Error("two writes of the same plaintext produced identical ciphertext")
	}

	// The same key written twice, which is the overwrite path rather than two
	// objects. Still a fresh nonce.
	before := inner.raw(t, first)
	if err := e.Put(t.Context(), team, first, []byte("same")); err != nil {
		t.Fatalf("Put again: %v", err)
	}
	if bytes.Equal(before, inner.raw(t, first)) {
		t.Error("rewriting the same object reused its nonce")
	}
}

// The object's own key is GCM's additional data, so bytes moved to a different
// key fail to open. Without it that swap decrypts perfectly — same team, same
// key, valid tag — and a person is served a different file than the row named.
func TestBytesMovedToAnotherKeyDoNotOpen(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	mine, theirs := objectKey(team, "mine"), objectKey(team, "theirs")

	if err := e.Put(t.Context(), team, mine, []byte("my file")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := e.Put(t.Context(), team, theirs, []byte("another file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Somebody with write access to the bucket copies one object's bytes over
	// another's. Both belong to this team and both are sealed under its key.
	if err := inner.Put(t.Context(), theirs, inner.raw(t, mine)); err != nil {
		t.Fatalf("moving the bytes: %v", err)
	}

	if _, err := e.Get(t.Context(), team, theirs); !errors.Is(err, ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt — the object key is not bound into the tag", err)
	}
}

// One team's key must not open another's object, which is the reason the key
// is per team rather than per deployment.
func TestAnotherTeamsKeyDoesNotOpenIt(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, perTeamKeys{})
	mine, theirs := uuid.New(), uuid.New()
	key := objectKey(mine, "abc")

	if err := e.Put(t.Context(), mine, key, []byte("my file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The other team asking for this exact key is refused before any crypto
	// happens, because the key names a team it does not belong to.
	if _, err := e.Get(t.Context(), theirs, key); !errors.Is(err, ErrWrongTeam) {
		t.Errorf("err = %v, want ErrWrongTeam", err)
	}
}

// The team is a parameter and the key is a database row. A row pointing at
// another team's object must be refused rather than resolved.
func TestAKeyOutsideTheTeamIsRefused(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	keys := newOneKey(0x11)
	e := newEncrypted(t, inner, keys)
	mine, theirs := uuid.New(), uuid.New()

	for name, key := range map[string]string{
		"another team's object": objectKey(theirs, "abc"),
		"no team at all":        "objects/abc",
		"a traversal":           TeamPrefix(mine) + "../" + TeamPrefix(theirs) + "abc",
		"the prefix alone":      TeamPrefix(mine),
		"empty":                 "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := e.Put(t.Context(), mine, key, []byte("x")); !errors.Is(err, ErrWrongTeam) {
				t.Errorf("Put err = %v, want ErrWrongTeam", err)
			}
			if _, err := e.Get(t.Context(), mine, key); !errors.Is(err, ErrWrongTeam) {
				t.Errorf("Get err = %v, want ErrWrongTeam", err)
			}
			if err := e.Delete(t.Context(), mine, key); !errors.Is(err, ErrWrongTeam) {
				t.Errorf("Delete err = %v, want ErrWrongTeam", err)
			}

			// The refusal must have changed nothing. An error alongside a
			// write that happened anyway is worse than either alone.
			inner.mu.Lock()
			defer inner.mu.Unlock()
			if len(inner.data) != 0 {
				t.Errorf("the store holds %d objects after a refusal", len(inner.data))
			}
		})
	}
}

// Reading an object that is not there must not generate a key for a team that
// has none. TeamKey creates on first use, so the order of the two calls in Get
// is behaviour rather than style.
func TestAMissingObjectDoesNotMintAKey(t *testing.T) {
	t.Parallel()

	keys := newOneKey(0x11)
	e := newEncrypted(t, newFakeStore(), keys)
	team := uuid.New()

	if _, err := e.Get(t.Context(), team, objectKey(team, "nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := keys.calls.Load(); n != 0 {
		t.Errorf("the key source was asked %d times for an object that is not there", n)
	}
}

// Bytes, not text. An attachment is a photograph or a PDF.
func TestBinaryContentSurvivesEncryption(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "abc")

	body := make([]byte, 256)
	for i := range body {
		body[i] = byte(i)
	}

	if err := e.Put(t.Context(), team, key, body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := e.Get(t.Context(), team, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the bytes that came back are not the bytes that went in")
	}
}

// An empty file is a file. It must round trip rather than being mistaken for
// an absent object at either end.
func TestAnEmptyFileRoundTrips(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, newOneKey(0x11))
	team := uuid.New()
	key := objectKey(team, "empty")

	if err := e.Put(t.Context(), team, key, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n := len(inner.raw(t, key)); n != 12+16 {
		t.Errorf("stored %d bytes for an empty file, want %d", n, 12+16)
	}

	got, err := e.Get(t.Context(), team, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get = %q, want nothing", got)
	}
}

func TestDeleteRemovesTheObject(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	keys := newOneKey(0x11)
	e := newEncrypted(t, inner, keys)
	team := uuid.New()
	key := objectKey(team, "abc")

	if err := e.Put(t.Context(), team, key, []byte("secret file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	before := keys.calls.Load()
	if err := e.Delete(t.Context(), team, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if keys.calls.Load() != before {
		t.Error("Delete fetched the team key, which it has no use for")
	}

	if _, err := e.Get(t.Context(), team, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, err = %v, want ErrNotFound", err)
	}
}

// A key source that cannot answer must fail the request rather than writing
// something unreadable or reading something wrong.
func TestAKeySourceFailureStopsTheOperation(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	keys := newOneKey(0x11)
	e := newEncrypted(t, inner, keys)
	team := uuid.New()
	key := objectKey(team, "abc")

	if err := e.Put(t.Context(), team, key, []byte("secret file")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keys.err = errors.New("the secret store is unreachable")

	if _, err := e.Get(t.Context(), team, key); err == nil {
		t.Error("Get succeeded with no key")
	}
	if err := e.Put(t.Context(), team, objectKey(team, "other"), []byte("x")); err == nil {
		t.Error("Put succeeded with no key")
	}

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if _, ok := inner.data[objectKey(team, "other")]; ok {
		t.Error("Put wrote an object it could not encrypt")
	}
}

// A key of the wrong length must be refused where it is named, not deep
// inside seal where the error mentions neither the team nor the path.
func TestAKeyOfTheWrongLengthIsRefused(t *testing.T) {
	t.Parallel()

	keys := newOneKey(0x11)
	keys.key = keys.key[:KeySize-1]

	e := newEncrypted(t, newFakeStore(), keys)
	team := uuid.New()

	err := e.Put(t.Context(), team, objectKey(team, "abc"), []byte("x"))
	if err == nil {
		t.Fatal("Put accepted a key that is not KeySize bytes")
	}
	if !strings.Contains(err.Error(), "key size") {
		t.Errorf("err = %v, want it to name the key size", err)
	}
}

// A store that cannot write is a boot failure, not a first-attachment
// failure — the same assertion serve.go makes about the writer.
func TestNewEncryptedRefusesAStoreThatCannotWrite(t *testing.T) {
	t.Parallel()

	_, err := NewEncrypted(readOnlyStore{}, newOneKey(0x11))
	if err == nil {
		t.Fatal("NewEncrypted accepted a read-only store")
	}
	if !strings.Contains(err.Error(), "cannot write") {
		t.Errorf("err = %v, want it to say the store cannot write", err)
	}
}

// Concurrent use, because one Encrypted is shared by every request. Under
// -race this is what catches state that should not be there.
func TestEncryptedIsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()

	inner := newFakeStore()
	e := newEncrypted(t, inner, perTeamKeys{})

	const writers = 16
	var wg sync.WaitGroup

	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			team := uuid.New()
			key := objectKey(team, fmt.Sprintf("obj-%d", i))
			body := fmt.Appendf(nil, "file number %d", i)

			if err := e.Put(context.Background(), team, key, body); err != nil {
				t.Errorf("Put: %v", err)
				return
			}
			got, err := e.Get(context.Background(), team, key)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			if !bytes.Equal(got, body) {
				t.Errorf("Get = %q, want %q", got, body)
			}
		}()
	}
	wg.Wait()
}
