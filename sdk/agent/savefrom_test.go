package agent

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// partialStore records where each save started, and keeps what fakeStore
// keeps, so a test reads both the call and the result.
type partialStore struct {
	*fakeStore
	froms []int
	fail  error
}

func (p *partialStore) SaveFrom(ctx context.Context, session string, from int, msgs []Message) error {
	p.froms = append(p.froms, from)
	if p.fail != nil {
		return p.fail
	}
	return p.Save(ctx, session, msgs)
}

func opened(t *testing.T, store Store) Session {
	t.Helper()
	s, err := Open("s1", store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestASaveThatOnlyAddedMessagesWritesFromWhereTheyStart(t *testing.T) {
	store := &partialStore{fakeStore: newFakeStore()}
	s := opened(t, store)
	msgs := conversation(2, func(int) string { return "x" }) // 5 messages

	_ = s.Save(t.Context(), msgs[:3])
	_ = s.Save(t.Context(), msgs)
	_ = s.Save(t.Context(), msgs)

	if !slices.Equal(store.froms, []int{0, 3, 5}) {
		t.Fatalf("saves started at %v; want 0, then 3 for the two added, then 5 for nothing new", store.froms)
	}
}

func TestASaveWritesFromTheFirstMessageThatChanged(t *testing.T) {
	store := &partialStore{fakeStore: newFakeStore()}
	s := opened(t, store)
	msgs := conversation(3, func(int) string { return "x" })
	_ = s.Save(t.Context(), msgs)

	// A summary spliced in after the task, as a compaction writes it.
	spliced := slices.Insert(slices.Clone(msgs), 3, markerMessage(CompactionSummary{Summary: "Goal: the task."}))
	_ = s.Save(t.Context(), spliced)

	if store.froms[1] != 3 {
		t.Fatalf("the save after a splice started at %d; want 3, where the transcript first changed", store.froms[1])
	}
}

func TestAResumedSessionWritesOnlyWhatItAdds(t *testing.T) {
	store := &partialStore{fakeStore: newFakeStore()}
	msgs := conversation(2, func(int) string { return "x" })
	_ = store.Save(t.Context(), "s1", msgs) // stored before the session opens, as a resume finds it

	s := opened(t, store)
	read, err := s.Messages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Save(t.Context(), append(read, say(RoleAssistant, "done")))

	if !slices.Equal(store.froms, []int{len(msgs)}) {
		t.Fatalf("a resumed session's first save started at %v; want %d, after what it read", store.froms, len(msgs))
	}
}

func TestAFailedSaveWritesEverythingNextTime(t *testing.T) {
	store := &partialStore{fakeStore: newFakeStore()}
	s := opened(t, store)
	msgs := conversation(2, func(int) string { return "x" })
	_ = s.Save(t.Context(), msgs[:3])

	store.fail = errors.New("the database went away")
	if err := s.Save(t.Context(), msgs); err == nil {
		t.Fatal("a failed save was reported as saved")
	}
	store.fail = nil
	_ = s.Save(t.Context(), msgs)

	if got := store.froms[len(store.froms)-1]; got != 0 {
		t.Fatalf("the save after a failure started at %d; what is stored is unknown, so it writes all", got)
	}
}

func TestAStoreWithoutSaveFromIsHandedEveryMessage(t *testing.T) {
	store := newFakeStore()
	s := opened(t, store)
	msgs := conversation(2, func(int) string { return "x" })

	_ = s.Save(t.Context(), msgs[:3])
	if err := s.Save(t.Context(), msgs); err != nil {
		t.Fatal(err)
	}

	if got := store.saved["s1"]; len(got) != len(msgs) {
		t.Fatalf("the store holds %d messages; a store without SaveFrom gets the whole transcript", len(got))
	}
}
