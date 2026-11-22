package keeper_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
)

func idle(context.Context) (bool, error) { return true, nil }

func TestAStoppedRoomIsResumedNotReplaced(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	first := w.ensure(k, want())
	room := rowOf(t, w).RoomID

	if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
		t.Fatal(err)
	}
	if !w.rooms.IsStopped(room) || rowOf(t, w).StoppedAt == nil {
		t.Fatal("the room or its row is not stopped")
	}

	again := w.ensure(k, want())
	if again.URL != first.URL || len(w.rooms.Created()) != 1 {
		t.Fatalf("a stopped room was replaced: %d rooms made", len(w.rooms.Created()))
	}
	if r := w.rooms.Resumes(); len(r) != 1 || r[0] != room {
		t.Fatalf("resumes = %v", r)
	}
	if rowOf(t, w).StoppedAt != nil {
		t.Fatal("the row still says stopped")
	}
	if call(t, again) != 200 {
		t.Fatal("the resumed room does not answer")
	}
}

// A caller found the session idle, but one came for the room before the
// lock was taken: the room is left running.
func TestABusyOwnerIsNotStopped(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	busy := func(context.Context) (bool, error) { return false, nil }
	if err := k.Stop(t.Context(), w.owner, w.team, busy); err != nil {
		t.Fatal(err)
	}
	if len(w.rooms.Stops()) != 0 || rowOf(t, w).StoppedAt != nil {
		t.Fatal("a room in use was stopped")
	}
}

func TestStoppingNoRoomOrAStoppedOneDoesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
		t.Fatalf("no room: %v", err)
	}
	w.ensure(k, want())
	for range 2 {
		if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.rooms.Stops()) != 1 {
		t.Fatalf("stops = %v, want one", w.rooms.Stops())
	}
}

// A provider with no room for it now leaves it stopped, its files kept,
// rather than replacing it with an empty one that would not fit either.
func TestARoomTheProviderIsTooFullForStaysStopped(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
		t.Fatal(err)
	}
	w.rooms.ResumeErr = sandbox.ErrFull
	if _, err := k.Ensure(t.Context(), w.owner, w.team, want()); !errors.Is(err, sandbox.ErrFull) {
		t.Fatalf("Ensure = %v, want ErrFull", err)
	}
	if len(w.rooms.Created()) != 1 || len(w.rooms.Deleted()) != 0 || rowOf(t, w).StoppedAt == nil {
		t.Fatal("a stopped room was replaced or deleted because the provider was full")
	}
}

func TestAStoppedRoomTheProviderLostIsReplaced(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	old := rowOf(t, w)
	if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
		t.Fatal(err)
	}
	w.rooms.Lose(old.RoomID)
	w.ensure(k, want())
	if row := rowOf(t, w); row.ID == old.ID || row.StoppedAt != nil || len(w.rooms.Created()) != 2 {
		t.Fatalf("row %+v, %d rooms made", row, len(w.rooms.Created()))
	}
}

func (w *world) sized(memory, cpus, disk int32) {
	w.t.Helper()
	_, err := w.store.Pool().Exec(w.t.Context(),
		`UPDATE team_settings SET sandbox_workspace_memory_mb = $2, sandbox_workspace_cpus = $3, sandbox_workspace_disk_mb = $4 WHERE team_id = $1`,
		w.team, memory, cpus, disk)
	if err != nil {
		w.t.Fatal(err)
	}
}

func TestAWorkspaceIsMadeAtItsTeamsSizeAndANewSizeIsANewRoom(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	ws := want()
	ws.Workspace = true
	w.sized(8192, 4, 4096)
	w.ensure(k, ws)
	if got := w.rooms.Created()[0].Size; got == nil || *got != (sandbox.Size{MemoryMB: 8192, CPUs: 4, DiskMB: 4096}) {
		t.Fatalf("size = %+v", got)
	}

	// A new size is a new room, without the old one's files: said once.
	w.sized(1024, 1, 512)
	if _, err := k.Ensure(t.Context(), w.owner, w.team, ws); !errors.Is(err, sandbox.ErrReplaced) {
		t.Fatalf("Ensure after a new size = %v, want ErrReplaced", err)
	}
	if n := len(w.rooms.Created()); n != 2 || *w.rooms.Created()[1].Size != (sandbox.Size{MemoryMB: 1024, CPUs: 1, DiskMB: 512}) {
		t.Fatalf("%d rooms; a new size did not give a new room", n)
	}
	w.ensure(k, ws)
	if n := len(w.rooms.Created()); n != 2 {
		t.Fatalf("%d rooms; the fresh room was not the one used next", n)
	}
}

func TestAServersRoomIsNeverSized(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.sized(8192, 4, 4096)
	w.ensure(w.keeper(), want())
	if got := w.rooms.Created()[0].Size; got != nil {
		t.Fatalf("a command server's room was sized %+v", got)
	}
}

func workspace() keeper.Want {
	w := want()
	w.Workspace = true
	return w
}

// The provider stopped the room but the row never heard (the mark failed,
// the run's end timed out, sandboxd stopped it over its disk): the next call
// resumes it, files and all, rather than replacing it.
func TestAWorkspaceStoppedBehindItsRowsBackIsResumed(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, workspace())
	room := rowOf(t, w).RoomID
	if err := w.rooms.Stop(t.Context(), room); err != nil {
		t.Fatal(err)
	}

	w.ensure(k, workspace())
	if r := w.rooms.Resumes(); len(r) != 1 || r[0] != room || len(w.rooms.Created()) != 1 {
		t.Fatalf("resumes %v, %d rooms made", r, len(w.rooms.Created()))
	}
}

// A workspace the provider lost while stopped is replaced, and the call that
// found it hears its files are gone; the next call uses the fresh room.
func TestALostWorkspaceIsReplacedAndSaidSo(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, workspace())
	old := rowOf(t, w)
	if err := k.Stop(t.Context(), w.owner, w.team, idle); err != nil {
		t.Fatal(err)
	}
	w.rooms.Lose(old.RoomID)

	if _, err := k.Ensure(t.Context(), w.owner, w.team, workspace()); !errors.Is(err, sandbox.ErrReplaced) {
		t.Fatalf("Ensure = %v, want ErrReplaced", err)
	}
	if row := rowOf(t, w); row.ID == old.ID || row.StoppedAt != nil || len(w.rooms.Created()) != 2 {
		t.Fatalf("row %+v, %d rooms made", row, len(w.rooms.Created()))
	}
	w.ensure(k, workspace())
	if len(w.rooms.Created()) != 2 {
		t.Fatal("the call after the replacement made yet another room")
	}
}

// A running workspace that does not answer is given a resume first; one that
// still does not answer is replaced, and said so.
func TestABrokenWorkspaceIsResumedThenReplaced(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, workspace())
	room := rowOf(t, w).RoomID
	w.rooms.Break(room)

	if _, err := k.Ensure(t.Context(), w.owner, w.team, workspace()); !errors.Is(err, sandbox.ErrReplaced) {
		t.Fatalf("Ensure = %v, want ErrReplaced", err)
	}
	if r := w.rooms.Resumes(); len(r) != 1 || r[0] != room {
		t.Fatalf("resumes = %v, want the broken room tried once", r)
	}
	if len(w.rooms.Created()) != 2 {
		t.Fatalf("%d rooms made", len(w.rooms.Created()))
	}
}

// A command server's room keeps no files: replacing it is said to no one.
func TestAServersLostRoomIsReplacedQuietly(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	k := w.keeper()
	w.ensure(k, want())
	w.rooms.Lose(rowOf(t, w).RoomID)
	w.ensure(k, want())
	if len(w.rooms.Created()) != 2 || len(w.rooms.Resumes()) != 0 {
		t.Fatalf("%d rooms, resumes %v", len(w.rooms.Created()), w.rooms.Resumes())
	}
}
