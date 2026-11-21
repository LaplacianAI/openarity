package runtime

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (f *execFixture) session(t *testing.T) uuid.UUID {
	t.Helper()
	var s uuid.UUID
	if err := f.raw.QueryRow(t.Context(), `SELECT session_id FROM runs WHERE id = $1`, f.run).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The last run of a session to end stops its workspace, which keeps its
// files and gives its memory back until the next call.
func TestTheLastRunOfASessionStopsItsWorkspace(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 100, 20)))
	ws := &fakeWorkspaces{room: newFakeRoom(t)}
	f.exec.builder.WithWorkspaces(&WorkspaceDeps{Rooms: ws})

	f.mustExecute(t)

	got := ws.stopped()
	if len(got) != 1 || got[0].session != f.session(t) || got[0].team != f.team || !got[0].idle {
		t.Fatalf("stops = %+v", got)
	}
}

// A run that ends while another of its session waits leaves the room
// running: the next one is about to use it.
func TestARunThatEndsBeforeAnotherOfItsSessionLeavesTheRoom(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 100, 20)))
	ws := &fakeWorkspaces{room: newFakeRoom(t)}
	f.exec.builder.WithWorkspaces(&WorkspaceDeps{Rooms: ws})
	if _, err := f.store.CreateRun(t.Context(), db.CreateRunParams{
		ID: uuid.New(), TeamID: f.team, SessionID: f.session(t), AgentID: f.agent, Input: "next",
	}); err != nil {
		t.Fatal(err)
	}

	f.mustExecute(t)

	for _, s := range ws.stopped() {
		if s.idle {
			t.Fatalf("a session with a run queued was stopped: %+v", s)
		}
	}
}

func TestARunWithNoWorkspacesStopsNothing(t *testing.T) {
	f := newExecFixture(t, newScripted(answer("42", 100, 20)))
	f.mustExecute(t) // no WorkspaceDeps: nothing to stop, and no panic
	wantStatus(t, f.row(t), "succeeded", "")
}
