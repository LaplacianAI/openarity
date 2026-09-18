package agent

import (
	"context"
	"strings"
	"testing"
)

// fakeStore is a test double, not a backend. The shipped stores live in their
// own packages under stores/ and are driven through stores/storetest; this one
// exists so the tests in this package can reach a Session without importing a
// package that imports this one.
type fakeStore struct {
	saved   map[string][]Message
	steers  map[string][]string
	takenBy []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{saved: map[string][]Message{}, steers: map[string][]string{}}
}

func (f *fakeStore) Save(_ context.Context, session string, msgs []Message) error {
	f.saved[session] = msgs
	return nil
}

func (f *fakeStore) Messages(_ context.Context, session string) ([]Message, error) {
	return f.saved[session], nil
}

func (f *fakeStore) AddSteer(_ context.Context, session, text string) error {
	f.steers[session] = append(f.steers[session], text)
	return nil
}

func (f *fakeStore) TakeSteers(_ context.Context, session string) ([]string, error) {
	f.takenBy = append(f.takenBy, session)
	out := f.steers[session]
	delete(f.steers, session)
	return out, nil
}

func TestASessionWithNoIDIsRefused(t *testing.T) {
	_, err := Open("", newFakeStore())
	if err == nil {
		t.Fatal("an empty id was accepted; every conversation would land in one bucket")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestASessionWithNoStoreIsRefused(t *testing.T) {
	if _, err := Open("s1", nil); err == nil {
		t.Fatal("a nil Store was accepted, so the first Save would panic mid-run")
	}
}

// The whole job of a Session is to be a Store with the id already filled in.
// Each of the four methods has to pass the id it was opened with, and getting
// one of them wrong would put a conversation's messages under "" while its
// steers went to the right place.
func TestEveryCallCarriesTheIDTheSessionWasOpenedWith(t *testing.T) {
	store := newFakeStore()
	session, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := session.Save(t.Context(), []Message{{Role: RoleUser}}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if len(store.saved["s1"]) != 1 {
		t.Errorf("Save() addressed %v, want s1", keys(store.saved))
	}

	back, err := session.Messages(t.Context())
	if err != nil {
		t.Fatalf("Messages() = %v", err)
	}
	if len(back) != 1 {
		t.Errorf("Messages() = %+v, want what Save was given — it asked under another id", back)
	}

	if err := session.Steer(t.Context(), "look in vault.go"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}
	if got := store.steers["s1"]; len(got) != 1 || got[0] != "look in vault.go" {
		t.Errorf("Steer() left %v under s1", got)
	}

	taken, err := session.TakeSteers(t.Context())
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	if len(taken) != 1 {
		t.Errorf("TakeSteers() = %v, want the steer just left", taken)
	}
	if len(store.takenBy) != 1 || store.takenBy[0] != "s1" {
		t.Errorf("TakeSteers() asked for %v, want s1", store.takenBy)
	}
}

func TestTwoSessionsOnOneStoreDoNotSeeEachOther(t *testing.T) {
	store := newFakeStore()
	first, err := Open("s1", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	second, err := Open("s2", store)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	if err := first.Steer(t.Context(), "for s1 only"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}

	got, err := second.TakeSteers(t.Context())
	if err != nil {
		t.Fatalf("TakeSteers() = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("s2 took %v, which was left for s1", got)
	}
}

func keys(m map[string][]Message) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
