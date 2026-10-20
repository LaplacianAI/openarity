package store

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func appendEvent(t *testing.T, s *Store, runID uuid.UUID, worker, kind string) (int32, error) {
	t.Helper()

	return s.AppendRunEvent(t.Context(), db.AppendRunEventParams{
		RunID: runID, Worker: worker, Kind: kind, Payload: []byte(`{}`),
	})
}

func seqs(events []db.RunEvent) []int32 {
	out := make([]int32, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

// A reader resumes a stream after the last number it saw, so a gap or a
// repeat is a lost or doubled event on somebody's screen.
func TestEventsAreNumberedFromOneWithoutGaps(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")

	for want := int32(1); want <= 3; want++ {
		got, err := appendEvent(t, s, r.ID, "w1", "step")
		if err != nil || got != want {
			t.Fatalf("event %d: seq %d, %v", want, got, err)
		}
	}
}

func TestEachRunIsNumberedOnItsOwn(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	a := createRun(t, s, f)
	b := createRun(t, s, f)
	claim(t, s, a.ID, "w1")
	claim(t, s, b.ID, "w1")

	_, _ = appendEvent(t, s, a.ID, "w1", "started")
	_, _ = appendEvent(t, s, a.ID, "w1", "step")
	got, err := appendEvent(t, s, b.ID, "w1", "started")
	if err != nil || got != 1 {
		t.Errorf("another run's first event is seq %d, %v; want 1", got, err)
	}
}

// After a takeover the worker that claims it next carries on the numbering, and the worker
// that lost the run can no longer write between its events.
func TestAWorkerThatLostTheRunCannotAppendAnEvent(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	_, _ = appendEvent(t, s, r.ID, "w1", "started")

	age(t, s, r.ID, "heartbeat_at", 120)
	if _, err := s.TakeOverStaleRuns(t.Context(), 90); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	claim(t, s, r.ID, "w2")

	_, err := appendEvent(t, s, r.ID, "w1", "step")
	wantNoRow(t, err, "the old worker's event after a takeover")

	got, err := appendEvent(t, s, r.ID, "w2", "resumed")
	if err != nil || got != 2 {
		t.Errorf("the new holder's first event is seq %d, %v; want 2", got, err)
	}
}

// The finished event is written after FinishRun has moved the status on, so
// the guard is the worker and not the status.
func TestTheHolderCanWriteTheFinishedEvent(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	if _, err := s.FinishRun(t.Context(), db.FinishRunParams{
		ID: r.ID, Worker: "w1", Status: "succeeded", Output: ptr("done"),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if _, err := appendEvent(t, s, r.ID, "w1", "finished"); err != nil {
		t.Errorf("the finished event after FinishRun: %v", err)
	}
}

func TestListRunEventsReadsAfterASequenceNumber(t *testing.T) {
	s := queryStore(t)
	f := newRunFixture(t, s, "platform")
	r := createRun(t, s, f)
	other := createRun(t, s, f)
	claim(t, s, r.ID, "w1")
	claim(t, s, other.ID, "w1")
	for range 5 {
		_, _ = appendEvent(t, s, r.ID, "w1", "step")
	}
	_, _ = appendEvent(t, s, other.ID, "w1", "step")

	page := func(after, size int32) []int32 {
		got, err := s.ListRunEvents(t.Context(), db.ListRunEventsParams{RunID: r.ID, After: after, PageSize: size})
		if err != nil {
			t.Fatalf("ListRunEvents(after %d): %v", after, err)
		}
		return seqs(got)
	}

	if got := page(0, 100); !slices.Equal(got, []int32{1, 2, 3, 4, 5}) {
		t.Errorf("from the start = %v, want 1..5 and nothing of another run", got)
	}
	if got := page(2, 2); !slices.Equal(got, []int32{3, 4}) {
		t.Errorf("after 2, two = %v, want [3 4]", got)
	}
	if got := page(5, 100); len(got) != 0 {
		t.Errorf("after the last = %v, want none", got)
	}
}
