package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func openWait(t *testing.T, s *Store, _ runFixture, run, _ uuid.UUID, reason string) db.RoomWait {
	t.Helper()
	w, err := s.WaitForRoom(t.Context(), run, reason, 60)
	if err != nil {
		t.Fatalf("WaitForRoom: %v", err)
	}
	return w
}

// A wait a person, a cancel or the hour ended is not opened again by a
// refusal the run meets before it is told; nor does a run that no longer
// runs open one.
func TestAWaitEndedByAnyoneButTheRoomIsNotReopened(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "room-reopen")
	for _, ended := range []string{"superseded", "cancelled", "gave_up"} {
		root := mustInsertRun(t, s, inState(f.root(), "running"))
		w := openWait(t, s, f, root, root, "it is full")
		exec(t, s, `UPDATE room_waits SET ended = '`+ended+`', ended_at = now() WHERE id = '`+w.ID.String()+`'`)
		if _, err := s.WaitForRoom(t.Context(), root, "it is full", 60); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("a wait ended %s was reopened: %v", ended, err)
		}
	}
	parked := mustInsertRun(t, s, inState(f.root(), "running"))
	park(t, s, parked)
	if _, err := s.WaitForRoom(t.Context(), parked, "it is full", 60); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a run that no longer runs opened a wait: %v", err)
	}
}

// A run waits for room once at a time: a second refusal is the same wait,
// keeping its time to try; one that ended before the run was told opens
// again, keeping when it began; once told, the next refusal is a new wait.
func TestARunHasOneWaitForRoomAtATime(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "room-waits")
	root := mustInsertRun(t, s, inState(f.root(), "running"))

	first := openWait(t, s, f, root, root, "full")
	exec(t, s, `UPDATE room_waits SET retry_at = now() + interval '5 minutes', since = now() - interval '3 minutes' WHERE id = '`+first.ID.String()+`'`)
	again := openWait(t, s, f, root, root, "still full")
	if again.ID != first.ID || again.Reason != "still full" || again.RetryAt.Sub(first.RetryAt) < 3*60e9 {
		t.Fatalf("second refusal = %+v, want the same wait, its try kept", again)
	}

	if n, err := s.EndRoomWait(t.Context(), db.EndRoomWaitParams{ID: first.ID, Ended: "room"}); err != nil || n != 1 {
		t.Fatalf("EndRoomWait = %d, %v", n, err)
	}
	if n, err := s.EndRoomWait(t.Context(), db.EndRoomWaitParams{ID: first.ID, Ended: "gave_up"}); err != nil || n != 0 {
		t.Fatalf("a wait ended twice: %d, %v", n, err)
	}
	reopened := openWait(t, s, f, root, root, "full again")
	if reopened.ID != first.ID || reopened.Ended != nil || reopened.EndedAt != nil || !reopened.Since.Equal(again.Since) {
		t.Fatalf("refused before it was told = %+v, want the wait open again since it began", reopened)
	}

	exec(t, s, `UPDATE room_waits SET ended = 'room', ended_at = now() WHERE id = '`+first.ID.String()+`'`)
	if err := s.MarkRoomWaitsTold(t.Context(), []uuid.UUID{first.ID}); err != nil {
		t.Fatal(err)
	}
	if next := openWait(t, s, f, root, root, "full"); next.ID == first.ID {
		t.Fatal("a refusal after the run was told reused the told wait")
	}
}

// A run awaits an untold wait for room, and whether a person is waited on
// is told apart: a child waiting for room is not one, a child waiting for
// approval is.
func TestRunAwaitsTellsARoomFromAPerson(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "awaits-room")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	awaits := func() db.RunAwaitsRow {
		t.Helper()
		got, err := s.RunAwaits(t.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := awaits(); got.Awaits || got.Person {
		t.Fatalf("a run with nothing = %+v", got)
	}

	w := openWait(t, s, f, root, root, "full")
	if got := awaits(); !got.Awaits || got.Person {
		t.Fatalf("its own wait = %+v, want awaits, no person", got)
	}
	exec(t, s, `UPDATE room_waits SET ended = 'room', ended_at = now() WHERE id = '`+w.ID.String()+`'`)
	if got := awaits(); !got.Awaits {
		t.Fatal("an ended wait not yet told does not await")
	}
	if err := s.MarkRoomWaitsTold(t.Context(), []uuid.UUID{w.ID}); err != nil {
		t.Fatal(err)
	}
	if got := awaits(); got.Awaits {
		t.Fatal("a told wait still awaits")
	}

	child := mustInsertRun(t, s, inState(f.child(root, "call_1#c"), "running"))
	exec(t, s, `UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now(), waiting_for = 'sandbox' WHERE id = '`+child.String()+`'`)
	if got := awaits(); !got.Awaits || got.Person {
		t.Fatalf("a child waiting for room = %+v, want no person", got)
	}
	exec(t, s, `UPDATE runs SET waiting_for = 'approval' WHERE id = '`+child.String()+`'`)
	if got := awaits(); !got.Awaits || !got.Person {
		t.Fatalf("a child waiting for approval = %+v, want a person", got)
	}
}

// What waits for room, and how long it has, as the sweep reads it.
func TestDueWaitsAndTheirBackoff(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "due-waits")
	root := mustInsertRun(t, s, inState(f.root(), "running"))
	w := openWait(t, s, f, root, root, "full")

	due := func() []db.ListDueRoomWaitsRow {
		t.Helper()
		got, err := s.ListDueRoomWaits(t.Context(), db.ListDueRoomWaitsParams{GiveUpSeconds: 3600, Batch: 10})
		if err != nil {
			t.Fatal(err)
		}
		var mine []db.ListDueRoomWaitsRow
		for _, d := range got {
			if d.RootRunID == root {
				mine = append(mine, d)
			}
		}
		return mine
	}
	if got := due(); len(got) != 0 {
		t.Fatalf("a wait a minute from its try is due: %+v", got)
	}
	exec(t, s, `UPDATE room_waits SET retry_at = now() WHERE id = '`+w.ID.String()+`'`)
	if got := due(); len(got) != 1 || got[0].GaveUp || got[0].SessionID != f.session || got[0].TeamID != f.team {
		t.Fatalf("due = %+v", got)
	}
	exec(t, s, `UPDATE room_waits SET since = now() - interval '1 hour' WHERE id = '`+w.ID.String()+`'`)
	if got := due(); len(got) != 1 || !got[0].GaveUp {
		t.Fatalf("an hour on = %+v, want given up", got)
	}

	if n, err := s.PushRoomWait(t.Context(), db.PushRoomWaitParams{ID: w.ID, MinSeconds: 60, MaxSeconds: 600}); err != nil || n != 1 {
		t.Fatalf("PushRoomWait = %d, %v", n, err)
	}
	if got := due(); len(got) != 0 {
		t.Fatal("a wait put off is still due")
	}
	if n, err := s.EndTreeRoomWaits(t.Context(), db.EndTreeRoomWaitsParams{RootRunID: root, Ended: "superseded"}); err != nil || n != 1 {
		t.Fatalf("EndTreeRoomWaits = %d, %v", n, err)
	}
	if n, err := s.PushRoomWait(t.Context(), db.PushRoomWaitParams{ID: w.ID, MinSeconds: 60, MaxSeconds: 600}); err != nil || n != 0 {
		t.Fatalf("an ended wait was put off: %d, %v", n, err)
	}
}

// A tree that waits on a person and on room is queued again only once both
// are done, whichever comes first.
func TestATreeWaitingOnAPersonAndRoomIsQueuedOnceBothAreDone(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRunFixture(t, s, "mixed-waits")
	for _, roomFirst := range []bool{true, false} {
		root := mustInsertRun(t, s, inState(f.root(), "running"))
		child := mustInsertRun(t, s, inState(f.child(root, "call_1#c"), "running"))
		a := mustAsk(t, s, asking(f, root, root, "call_2", "ABCD"))
		w := openWait(t, s, f, child, root, "it is full")
		park(t, s, root)

		endRoom := func() {
			exec(t, s, `UPDATE room_waits SET ended = 'room', ended_at = now() WHERE id = '`+w.ID.String()+`'`)
		}
		first, second := endRoom, func() { approve(t, s, a) }
		if !roomFirst {
			first, second = second, first
		}
		first()
		if _, err := s.RequeueAnswered(t.Context(), root); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("room first %v: queued with one of the two still waiting: %v", roomFirst, err)
		}
		second()
		if _, err := s.RequeueAnswered(t.Context(), root); err != nil {
			t.Fatalf("room first %v: not queued once both were done: %v", roomFirst, err)
		}
	}
}
