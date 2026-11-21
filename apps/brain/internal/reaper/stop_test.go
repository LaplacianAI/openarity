package reaper

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// workspace is a session's room, ensured as the runtime ensures one.
func (w *roomsWorld) workspace() (db.SandboxRoom, uuid.UUID) {
	w.t.Helper()
	var session uuid.UUID
	if err := w.store.Pool().QueryRow(w.t.Context(),
		`INSERT INTO sessions (team_id, kind) VALUES ($1, 'group') RETURNING id`, w.team).Scan(&session); err != nil {
		w.t.Fatal(err)
	}
	_, err := w.keeper.Ensure(w.t.Context(), keeper.Owner{Kind: "session", ID: session}, w.team, keeper.Want{
		Argv: []string{"sandbox", "workspace"}, Network: sandbox.NetworkPublic, Workspace: true,
		Port: 8081, HealthPath: "/healthz", TokenEnv: tokenEnv,
	})
	if err != nil {
		w.t.Fatalf("Ensure: %v", err)
	}
	row, err := w.store.GetSandboxRoom(w.t.Context(), db.GetSandboxRoomParams{OwnerKind: "session", OwnerID: session})
	if err != nil {
		w.t.Fatal(err)
	}
	return row, session
}

func (w *roomsWorld) row(kind string, owner uuid.UUID) (db.SandboxRoom, bool) {
	w.t.Helper()
	row, err := w.store.GetSandboxRoom(w.t.Context(), db.GetSandboxRoomParams{OwnerKind: kind, OwnerID: owner})
	return row, err == nil
}

func (w *roomsWorld) set(query string, args ...any) {
	w.t.Helper()
	if _, err := w.store.Pool().Exec(w.t.Context(), query, args...); err != nil {
		w.t.Fatal(err)
	}
}

// An idle workspace is stopped, keeping its files, not deleted.
func TestAnIdleWorkspaceIsStoppedNotDeleted(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room, session := w.workspace()
	w.idleSince(room, time.Now().Add(-2*idleFor))

	w.sweep()

	row, ok := w.row("session", session)
	if !ok || row.StoppedAt == nil || !w.rooms.IsStopped(room.RoomID) || len(w.rooms.Deleted()) != 0 {
		t.Fatalf("row %v (kept %v), stopped %v, deleted %v", row.StoppedAt, ok, w.rooms.IsStopped(room.RoomID), w.rooms.Deleted())
	}
}

// A team's own idle minutes win over the deployment's.
func TestATeamsIdleMinutesAreItsOwn(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room, session := w.workspace()
	w.set(`UPDATE team_settings SET sandbox_idle_minutes = 600 WHERE team_id = $1`, w.team)
	w.idleSince(room, time.Now().Add(-2*idleFor))

	w.sweep()

	if row, _ := w.row("session", session); row.StoppedAt != nil {
		t.Fatal("a room within its team's own idle minutes was stopped")
	}
}

// A session with a run still going is not stopped, however long the room
// has been unused.
func TestAnIdleWorkspaceOfABusySessionIsLeft(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room, session := w.workspace()
	w.idleSince(room, time.Now().Add(-2*idleFor))
	var agent uuid.UUID
	if err := w.store.Pool().QueryRow(t.Context(), `
		INSERT INTO agents (team_id, name, model_name, max_tokens, pattern, max_steps)
		VALUES ($1, 'a', 'm', 1024, 'react', 10) RETURNING id`, w.team).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.CreateRun(t.Context(), db.CreateRunParams{ID: uuid.New(), TeamID: w.team, SessionID: session, AgentID: agent, Input: "x"}); err != nil {
		t.Fatal(err)
	}

	w.sweep()

	if row, _ := w.row("session", session); row.StoppedAt != nil || w.rooms.IsStopped(room.RoomID) {
		t.Fatal("a room whose session has a run queued was stopped")
	}
}

// A stopped workspace past its team's keep hours is deleted with its token.
func TestAStoppedWorkspacePastItsKeepHoursIsDeleted(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room, session := w.workspace()
	w.set(`UPDATE team_settings SET sandbox_keep_hours = 2 WHERE team_id = $1`, w.team)
	w.set(`UPDATE sandbox_rooms SET stopped_at = now() - interval '1 hour' WHERE id = $1`, room.ID)
	w.sweep()
	if _, ok := w.row("session", session); !ok {
		t.Fatal("a room within its keep hours was deleted")
	}

	w.set(`UPDATE sandbox_rooms SET stopped_at = now() - interval '3 hours' WHERE id = $1`, room.ID)
	w.sweep()
	if _, ok := w.row("session", session); ok {
		t.Fatal("a room past its keep hours is still there")
	}
	if d := w.rooms.Deleted(); len(d) != 1 || d[0] != room.RoomID {
		t.Fatalf("deleted = %v", d)
	}
}

// A command server's idle room is still deleted, never stopped.
func TestACommandServersIdleRoomIsStillDeleted(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room := w.ensure()
	w.idleSince(room, time.Now().Add(-2*idleFor))
	w.sweep()
	if _, ok := w.row("mcp_server", w.server.ID); ok || len(w.rooms.Stops()) != 0 {
		t.Fatalf("kept %v, stops %v", ok, w.rooms.Stops())
	}
}

// A workspace already stopped is not listed again, so the rooms that are
// stopped do not take the places of the ones still running in a sweep.
func TestAStoppedWorkspaceIsNotListedAsIdle(t *testing.T) {
	t.Parallel()
	w := newRoomsWorld(t)
	room, _ := w.workspace()
	w.idleSince(room, time.Now().Add(-2*idleFor))
	w.set(`UPDATE sandbox_rooms SET stopped_at = now() WHERE id = $1`, room.ID)

	rows, err := w.store.ListIdleWorkspaceRooms(t.Context(), db.ListIdleWorkspaceRoomsParams{DefaultMinutes: 1, Max: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == room.ID {
			t.Fatal("a stopped workspace was listed as idle")
		}
	}
}
