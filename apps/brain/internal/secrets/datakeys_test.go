package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// A store held in the test rather than internal/secrets/static, which imports
// this package — reaching back for it here would be an import cycle.
type memStore struct {
	mu   sync.Mutex
	data map[string]map[string]string

	// getErr is returned by every Get, for the branch where the secret store
	// is unreachable rather than empty.
	getErr error

	// hideNextGet makes exactly one Get answer ErrNotFound whatever is
	// stored, which is how the lost side of the race is reproduced without
	// depending on goroutine scheduling.
	hideNextGet atomic.Bool

	// onGet runs at the end of every Get, so a test can hold every reader at
	// the same point rather than hoping the scheduler produces a collision.
	onGet func()
}

func newMemStore() *memStore {
	return &memStore{data: map[string]map[string]string{}}
}

func (m *memStore) Get(_ context.Context, path, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	if m.hideNextGet.CompareAndSwap(true, false) {
		return "", ErrNotFound
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	value, ok := m.data[path][key]

	if m.onGet != nil {
		m.mu.Unlock()
		m.onGet()
		m.mu.Lock()
	}

	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (m *memStore) Put(_ context.Context, path, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.set(path, key, value)
	return nil
}

func (m *memStore) Create(_ context.Context, path, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.data[path][key]; ok {
		return ErrExists
	}
	m.set(path, key, value)
	return nil
}

func (m *memStore) Delete(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, path)
	return nil
}

func (m *memStore) set(path, key, value string) {
	if m.data[path] == nil {
		m.data[path] = map[string]string{}
	}
	m.data[path][key] = value
}

func (m *memStore) stored(t *testing.T, path, key string) string {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	value, ok := m.data[path][key]
	if !ok {
		t.Fatalf("nothing stored at %s/%s", path, key)
	}
	return value
}

// readOnlyStore implements Store and nothing else.
type readOnlyStore struct{}

func (readOnlyStore) Get(context.Context, string, string) (string, error) {
	return "", ErrNotFound
}

const testKeySize = 32

func newKeys(t *testing.T, store Store) *DataKeys {
	t.Helper()

	keys, err := NewDataKeys(store, testKeySize)
	if err != nil {
		t.Fatalf("NewDataKeys: %v", err)
	}
	return keys
}

func TestTheFirstCallGeneratesAKeyAndStoresIt(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	keys := newKeys(t, store)
	team := uuid.New()

	key, err := keys.TeamKey(t.Context(), team)
	if err != nil {
		t.Fatalf("TeamKey: %v", err)
	}
	if len(key) != testKeySize {
		t.Fatalf("key is %d bytes, want %d", len(key), testKeySize)
	}

	// Where it went matters: the path is what the deployed OpenBao policy
	// grants, so a change here is a 403 in production rather than a test
	// failure unless it is pinned.
	stored := store.stored(t, TeamPath(team, KindAttachments), dataKeyField)

	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		t.Fatalf("what was stored is not base64: %v", err)
	}
	if !bytes.Equal(decoded, key) {
		t.Error("the key returned is not the key that was stored")
	}
}

// A key is decided once. Every later call agrees with that decision, or every
// object written before it becomes unreadable.
func TestTheSameTeamAlwaysGetsTheSameKey(t *testing.T) {
	t.Parallel()

	keys := newKeys(t, newMemStore())
	team := uuid.New()

	first, err := keys.TeamKey(t.Context(), team)
	if err != nil {
		t.Fatalf("TeamKey: %v", err)
	}
	second, err := keys.TeamKey(t.Context(), team)
	if err != nil {
		t.Fatalf("TeamKey again: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Error("two calls for one team returned different keys")
	}
}

// The reason the key is per team: one team's key must not open another's
// objects even when the same store hands out both.
func TestTwoTeamsGetDifferentKeys(t *testing.T) {
	t.Parallel()

	keys := newKeys(t, newMemStore())

	mine, err := keys.TeamKey(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("TeamKey: %v", err)
	}
	theirs, err := keys.TeamKey(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("TeamKey: %v", err)
	}

	if bytes.Equal(mine, theirs) {
		t.Error("two teams were given the same key")
	}
}

// The lost side of the race, reproduced deterministically: the read says
// nothing is there, and by the time Create runs somebody else has written.
// The loser must return the winner's key rather than its own.
func TestTheLoserOfTheRaceReturnsTheWinnersKey(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	keys := newKeys(t, store)
	team := uuid.New()

	winner, err := keys.TeamKey(t.Context(), team)
	if err != nil {
		t.Fatalf("the winner: %v", err)
	}

	// Now make one read pretend the key is absent, which is exactly what the
	// loser saw before the winner wrote.
	store.hideNextGet.Store(true)

	loser, err := keys.TeamKey(t.Context(), team)
	if err != nil {
		t.Fatalf("the loser: %v", err)
	}

	if !bytes.Equal(loser, winner) {
		t.Error("the loser generated its own key, so anything the winner " +
			"sealed can no longer be opened")
	}
}

// The same thing without the fixture, run for real. Every caller must come
// away with one key, and the key must be the one in the store.
func TestConcurrentFirstUploadsAgreeOnOneKey(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	keys := newKeys(t, store)
	team := uuid.New()

	const callers = 16
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got = make([][]byte, 0, callers)
	)

	// Hold every caller until all of them have read, so all of them see no
	// key and all of them reach Create. Left to the scheduler the winner
	// writes first and the other fifteen find the key on their opening read,
	// never touching the branch this test exists for — measured: mutating
	// the ErrExists arm to keep its own key left this test passing.
	var seen atomic.Int64
	release := make(chan struct{})
	store.onGet = func() {
		switch n := seen.Add(1); {
		case n == callers:
			close(release)
		case n < callers:
			<-release
		}
	}

	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			key, err := keys.TeamKey(context.Background(), team)
			if err != nil {
				t.Errorf("TeamKey: %v", err)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			got = append(got, key)
		}()
	}
	close(start)
	wg.Wait()

	if len(got) != callers {
		t.Fatalf("%d callers returned a key, want %d", len(got), callers)
	}
	for i, key := range got {
		if !bytes.Equal(key, got[0]) {
			t.Fatalf("caller %d came away with a different key", i)
		}
	}

	stored, err := base64.StdEncoding.DecodeString(
		store.stored(t, TeamPath(team, KindAttachments), dataKeyField))
	if err != nil {
		t.Fatalf("decoding what was stored: %v", err)
	}
	if !bytes.Equal(stored, got[0]) {
		t.Error("every caller agreed on a key that is not the one in the store")
	}
}

// A key that cannot be used must be refused where it is named. Left to
// aes.NewCipher it becomes "invalid key size 31" from inside seal, naming
// neither the team nor the path to fix.
func TestAStoredKeyThatCannotBeUsedIsRefusedByName(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	path := TeamPath(team, KindAttachments)

	for name, tc := range map[string]struct{ value, wants string }{
		"not base64":  {"not base64 at all!!", "not base64"},
		"too short":   {base64.StdEncoding.EncodeToString(make([]byte, 31)), "31 bytes"},
		"too long":    {base64.StdEncoding.EncodeToString(make([]byte, 33)), "33 bytes"},
		"empty value": {"", "0 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := newMemStore()
			if err := store.Put(t.Context(), path, dataKeyField, tc.value); err != nil {
				t.Fatalf("seeding: %v", err)
			}

			_, err := newKeys(t, store).TeamKey(t.Context(), team)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error does not name the path: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not say %q: %v", tc.wants, err)
			}
		})
	}
}

// An unreachable store must not look like an empty one. Generating a key
// because a read failed would write a second key for a team that has one.
func TestAnUnreadableStoreDoesNotGenerateAKey(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	store.getErr = ErrUnavailable
	team := uuid.New()

	if _, err := newKeys(t, store).TeamKey(t.Context(), team); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.data) != 0 {
		t.Error("a key was generated for a team whose key could not be read")
	}
}

// A store that can only overwrite would give two teams-worth of first uploads
// two different keys and keep the second. That is a startup failure, not a
// first-attachment failure.
func TestNewDataKeysRefusesAStoreThatCannotCreate(t *testing.T) {
	t.Parallel()

	_, err := NewDataKeys(readOnlyStore{}, testKeySize)
	if err == nil {
		t.Fatal("NewDataKeys accepted a store that cannot create")
	}
	if !strings.Contains(err.Error(), "without replacing") {
		t.Errorf("err = %v, want it to say why Put is not enough", err)
	}
}

func TestNewDataKeysRefusesAnImpossibleSize(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -1} {
		if _, err := NewDataKeys(newMemStore(), size); err == nil {
			t.Errorf("NewDataKeys accepted size %d", size)
		}
	}
}
