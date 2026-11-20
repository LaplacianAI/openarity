package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// workspaces is 20261201090000_workspaces.sql, which lets a session own a
// room and an agent be granted one; the migration before it is
// addImprovements.
const workspaces = 20261201090000

// sessionRoom is a session of f's team with a workspace room.
func (f roomFixture) sessionRoom(t *testing.T, s *Store) (uuid.UUID, db.SandboxRoom) {
	t.Helper()
	ch := scalar[uuid.UUID](t, s, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'custom', 'support') RETURNING id`, f.team.ID)
	session := scalar[uuid.UUID](t, s,
		`INSERT INTO sessions (team_id, channel_id, provider_ref, kind) VALUES ($1, $2, $3, 'group') RETURNING id`,
		f.team.ID, ch, "c-"+uuid.NewString())
	room, err := s.CreateSandboxRoom(t.Context(), db.CreateSandboxRoomParams{
		ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "session", OwnerID: session,
		Provider: "sandboxd", ProviderUrl: ptrTo("https://sbx.example"), RoomID: "room-" + session.String(),
		SpecHash: "hash", SandboxSecretID: f.key,
	})
	if err != nil {
		t.Fatalf("CreateSandboxRoom for a session: %v", err)
	}
	return session, room
}

// A session deleted takes its workspace's row, and the room is recorded for
// deletion with its token, as a command server's is; another owner's room
// stays.
func TestDeletingTheSessionTombstonesItsWorkspace(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-session")
	session, room := f.sessionRoom(t, s)

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id = $1`, session); err != nil {
		t.Fatalf("deleting the session: %v", err)
	}
	if _, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "session", OwnerID: session}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("the deleted session's room row: %v, want none", err)
	}
	if _, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: f.server.ID}); err != nil {
		t.Errorf("the server's room row: %v, want it kept", err)
	}
	if rooms := deletedRooms(t, s); len(rooms) != 1 || rooms[0].RoomID != room.RoomID {
		t.Errorf("tombstones = %+v, want only the session's room", rooms)
	}
	if paths := secretPaths(t, s); !slices.Contains(paths, secrets.RoomPath(f.team.ID, room.ID)) {
		t.Errorf("secret tombstones = %v, want the room's token", paths)
	}
}

// A room may be owned only by what the database knows deletes it.
func TestARoomOfAnUnknownOwnerIsRefused(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-owner")
	_, err := s.CreateSandboxRoom(t.Context(), db.CreateSandboxRoomParams{
		ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "run", OwnerID: uuid.New(),
		Provider: "sandboxd", RoomID: "room-x", SpecHash: "hash", SandboxSecretID: f.key,
	})
	if err == nil {
		t.Fatal("a room owned by a run was stored, and nothing would ever delete it")
	}
}

// Touched by owner, a session's room is in use; another's is not touched.
func TestTouchingByOwnerKeepsOnlyThatRoomInUse(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-touch")
	session, room := f.sessionRoom(t, s)
	if _, err := s.pool.Exec(t.Context(), `UPDATE sandbox_rooms SET last_used_at = now() - interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSandboxRoomByOwner(t.Context(), db.TouchSandboxRoomByOwnerParams{OwnerKind: "session", OwnerID: session}); err != nil {
		t.Fatalf("TouchSandboxRoomByOwner: %v", err)
	}
	if !scalar[bool](t, s, `SELECT last_used_at > now() - interval '1 minute' FROM sandbox_rooms WHERE id = $1`, room.ID) {
		t.Error("the touched room is still idle")
	}
	if scalar[bool](t, s, `SELECT last_used_at > now() - interval '1 minute' FROM sandbox_rooms WHERE id = $1`, f.room.ID) {
		t.Error("another owner's room was touched")
	}
}

// Rolled back, sessions' rooms go as rooms go, tombstoned for the sweep; the
// grant and the trigger go; migrated again, it all comes back.
func TestWorkspacesRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	f := newRoomFixture(t, s, "workspaces-down")
	_, room := f.sessionRoom(t, s)

	downTo(t, s, addImprovements)

	if n := count(t, s, `SELECT count(*) FROM sandbox_rooms WHERE id = $1`, room.ID); n != 0 {
		t.Error("rolled back, a session's room row is still there")
	}
	if n := count(t, s, `SELECT count(*) FROM deleted_rooms WHERE room_id = $1`, room.RoomID); n != 1 {
		t.Errorf("rolled back, the session's room has %d tombstones, want 1", n)
	}
	if n := count(t, s, `SELECT count(*) FROM sandbox_rooms WHERE id = $1`, f.room.ID); n != 1 {
		t.Error("rolled back, the server's room went too")
	}
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'agents' AND column_name = 'workspace'`); n != 0 {
		t.Error("rolled back, agents.workspace is still there")
	}
	if n := count(t, s, `SELECT count(*) FROM pg_trigger WHERE tgname = 'sessions_tombstone_rooms'`); n != 0 {
		t.Error("rolled back, the sessions trigger is still there")
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after rolling back: %v", err)
	}
	if n := count(t, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'agents' AND column_name = 'workspace'`); n != 1 {
		t.Error("migrated again, agents.workspace is not there")
	}
}
