package reaper

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/sandboxtest"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

const (
	idleFor  = 30 * time.Minute
	tokenEnv = "ROOM_TOKEN"
)

type bothSecrets struct {
	secrets.Store
	secrets.Writer
}

// roomsWorld is a team with a sandbox and a command server's room made by the
// keeper, with the reaper's effects over the same database, provider and
// secret store: the lifecycle of a room from first call to erasure.
type roomsWorld struct {
	t       *testing.T
	store   *store.Store
	secrets bothSecrets
	rooms   *sandboxtest.Rooms
	keeper  *keeper.Keeper
	team    uuid.UUID
	key     uuid.UUID
	server  db.McpServer

	mu     sync.Mutex
	opened []string // the key of every client the keeper opened
}

func newRoomsWorld(t *testing.T) *roomsWorld {
	t.Helper()

	dsn := storetest.Migrated(t, func(ctx context.Context, dsn string) error {
		s, err := store.New(ctx, dsn)
		if err != nil {
			return err
		}
		defer s.Close()
		_, err = s.Migrate(ctx)
		return err
	})
	s, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)

	team, err := s.CreateTeam(t.Context(), "reaped-rooms")
	if err != nil {
		t.Fatal(err)
	}
	st := static.New()
	writer, ok := st.(secrets.Writer)
	if !ok {
		t.Fatal("the static store cannot write")
	}
	w := &roomsWorld{
		t: t, store: s, secrets: bothSecrets{st, writer},
		rooms: sandboxtest.New(t, tokenEnv), team: team.ID,
	}
	w.keeper = keeper.New(s, w.secrets, func(_ sandbox.Provider, _, key string) (sandbox.Rooms, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.opened = append(w.opened, key)
		return w.rooms, nil
	}, http.DefaultClient, keeper.Images{}, discardLogger()).Starting(2*time.Second, 10*time.Millisecond)

	w.key = w.saveSandbox("first-key")
	w.server, err = s.CreateMCPServer(t.Context(), db.CreateMCPServerParams{
		TeamID: team.ID, Name: "mail", Command: []string{"npx", "mail"}, Env: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// saveSandbox saves the team's sandbox with a new key, as the settings route
// does: the key in the secret store first, then the row that names it.
func (w *roomsWorld) saveSandbox(key string) uuid.UUID {
	w.t.Helper()

	id := uuid.New()
	if err := w.secrets.Put(w.t.Context(), secrets.Path(w.team, secrets.KindSandbox, id), keeper.KeyField, key); err != nil {
		w.t.Fatal(err)
	}
	url := "https://sbx.example"
	_, err := w.store.UpsertTeamSettings(w.t.Context(), db.UpsertTeamSettingsParams{
		TeamID: w.team, SearchProvider: "none", CompactionMessages: 100,
		SandboxProvider: "sandboxd", SandboxUrl: &url, SandboxSecretID: &id,
	})
	if err != nil {
		w.t.Fatalf("UpsertTeamSettings: %v", err)
	}
	return id
}

func (w *roomsWorld) ensure() db.SandboxRoom {
	w.t.Helper()

	_, err := w.keeper.Ensure(w.t.Context(), keeper.Owner{Kind: "mcp_server", ID: w.server.ID}, w.team, keeper.Want{
		Argv: []string{"sandbox", "shim", "--", "npx", "mail"}, Port: sandbox.ShimPort, Path: "/mcp",
		HealthPath: "/healthz", TokenEnv: tokenEnv,
	})
	if err != nil {
		w.t.Fatalf("Ensure: %v", err)
	}
	row, err := w.store.GetSandboxRoom(w.t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: w.server.ID})
	if err != nil {
		w.t.Fatal(err)
	}
	return row
}

// sweep runs the room effect and then the secret effect, as the worker's
// sweep does with the rooms first.
func (w *roomsWorld) sweep() (rooms, keys Result) {
	w.t.Helper()

	var err error
	rooms, err = New(Rooms(w.store, w.keeper, idleFor), discardLogger()).Sweep(w.t.Context())
	if err != nil {
		w.t.Fatalf("sweeping rooms: %v", err)
	}
	keys = w.sweepSecrets()
	return rooms, keys
}

func (w *roomsWorld) sweepSecrets() Result {
	w.t.Helper()

	keys, err := New(Secrets(w.store, w.secrets), discardLogger()).Sweep(w.t.Context())
	if err != nil {
		w.t.Fatalf("sweeping secrets: %v", err)
	}
	return keys
}

func (w *roomsWorld) idleSince(room db.SandboxRoom, at time.Time) {
	w.t.Helper()
	if _, err := w.store.Pool().Exec(w.t.Context(), `UPDATE sandbox_rooms SET last_used_at = $2 WHERE id = $1`, room.ID, at); err != nil {
		w.t.Fatal(err)
	}
}

func (w *roomsWorld) secretExists(path string) bool {
	_, err := w.secrets.Get(w.t.Context(), path, keeper.KeyField)
	if errors.Is(err, secrets.ErrNotFound) {
		_, err = w.secrets.Get(w.t.Context(), path, keeper.TokenKey)
	}
	return err == nil
}

// A room nobody has called for the idle window is deleted at the provider,
// with its token; one used since is left, and the next call makes a new room.
func TestAnIdleRoomIsDeletedAndAFreshOneIsLeft(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()

	if res, _ := w.sweep(); res.Applied != 0 || !w.rooms.Has(room.RoomID) {
		t.Fatalf("a room used just now was swept: %+v", res)
	}

	w.idleSince(room, time.Now().Add(-idleFor-time.Minute))
	res, _ := w.sweep()

	if res.Applied != 1 || res.Failed != 0 {
		t.Errorf("result = %+v, want the idle room applied", res)
	}
	if w.rooms.Has(room.RoomID) {
		t.Error("the idle room is still at the provider")
	}
	if w.secretExists(secrets.RoomPath(w.team, room.ID)) {
		t.Error("the idle room's token was not destroyed")
	}
	if res.Outstanding != 0 {
		t.Errorf("%d tombstones outstanding after the sweep", res.Outstanding)
	}

	// The owner is called again: a new room, not an error.
	if again := w.ensure(); again.RoomID == room.RoomID || !w.rooms.Has(again.RoomID) {
		t.Errorf("room = %+v, want a fresh one", again)
	}
}

// A server deleted has its room deleted: the row going is what the sweep
// acts on, since no cascade reaches a provider.
func TestADeletedServersRoomIsDeleted(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()
	other := w.rooms.Live()

	if err := w.store.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
		t.Fatal(err)
	}
	res, _ := w.sweep()

	if res.Applied != 1 || w.rooms.Has(room.RoomID) || other != 1 {
		t.Errorf("result = %+v, room still there = %v", res, w.rooms.Has(room.RoomID))
	}
	if w.secretExists(secrets.RoomPath(w.team, room.ID)) {
		t.Error("the room's token was not destroyed")
	}
}

// A team that saves a new key has its room deleted with the old key, which is
// the only one that opens the provider the room is at. The old key's own
// tombstone waits: swept first, it is kept; once the room is gone, it goes.
func TestARoomIsDeletedWithTheKeyItWasMadeWithAndTheKeyWaitsForIt(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()
	oldKeyPath := secrets.Path(w.team, secrets.KindSandbox, w.key)
	w.saveSandbox("second-key")

	// Keys first, rooms not yet: the key is held.
	w.sweepSecrets()
	if !w.secretExists(oldKeyPath) {
		t.Fatal("the key was destroyed while its room was still at the provider")
	}
	if !w.rooms.Has(room.RoomID) {
		t.Fatal("the room was deleted without a sweep")
	}

	w.mu.Lock()
	w.opened = nil
	w.mu.Unlock()
	res, _ := w.sweep()

	if res.Applied != 1 || w.rooms.Has(room.RoomID) {
		t.Errorf("result = %+v, room still there = %v", res, w.rooms.Has(room.RoomID))
	}
	w.mu.Lock()
	opened := append([]string(nil), w.opened...)
	w.mu.Unlock()
	if len(opened) == 0 || opened[0] != "first-key" {
		t.Errorf("the room was deleted with %v, want the key it was made with", opened)
	}

	// The sweep after the rooms' took the key.
	w.sweepSecrets()
	if w.secretExists(oldKeyPath) {
		t.Error("the old key outlived its room")
	}
}

// Turning the sandbox off is a change of provider like another.
func TestTurningTheSandboxOffDeletesItsRooms(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()
	if _, err := w.store.UpsertTeamSettings(t.Context(), db.UpsertTeamSettingsParams{
		TeamID: w.team, SearchProvider: "none", CompactionMessages: 100, SandboxProvider: "none",
	}); err != nil {
		t.Fatal(err)
	}

	res, _ := w.sweep()

	if res.Applied != 1 || w.rooms.Has(room.RoomID) {
		t.Errorf("result = %+v, room still there = %v", res, w.rooms.Has(room.RoomID))
	}
	if w.secretExists(secrets.Path(w.team, secrets.KindSandbox, w.key)) {
		t.Error("the key was not destroyed once its room was gone")
	}
}

// A provider that refuses the delete leaves the tombstone to be tried again,
// and the key it needs stays.
func TestARoomTheProviderWillNotDeleteIsKept(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()
	if err := w.store.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
		t.Fatal(err)
	}
	w.rooms.DeleteErr = errors.New("the provider is down")

	res, _ := w.sweep()

	if res.Failed != 1 || res.Outstanding != 1 {
		t.Errorf("result = %+v, want one failure and one tombstone owed", res)
	}
	if !w.rooms.Has(room.RoomID) {
		t.Error("the room was deleted")
	}
}

// If the key is gone, nothing can authenticate to the provider: the tombstone
// is cleared rather than retried for ever.
func TestARoomWhoseKeyIsGoneIsLetGo(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	w.ensure()
	if err := w.store.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.secrets.Delete(t.Context(), secrets.Path(w.team, secrets.KindSandbox, w.key)); err != nil {
		t.Fatal(err)
	}

	rooms, _ := w.sweep()

	if rooms.Superseded != 1 || rooms.Failed != 0 || rooms.Outstanding != 0 {
		t.Errorf("result = %+v, want the tombstone superseded and cleared", rooms)
	}
}

// A room the provider no longer has is not a failure.
func TestARoomAlreadyGoneIsApplied(t *testing.T) {
	t.Parallel()

	w := newRoomsWorld(t)
	room := w.ensure()
	w.rooms.Lose(room.RoomID)
	if err := w.store.DeleteMCPServer(t.Context(), w.server.ID); err != nil {
		t.Fatal(err)
	}

	rooms, _ := w.sweep()

	if rooms.Applied != 1 || rooms.Failed != 0 {
		t.Errorf("result = %+v", rooms)
	}
}

func TestTheRoomEffectNamesItself(t *testing.T) {
	t.Parallel()

	if got := (roomEffect{}).Name(); got != "rooms" {
		t.Errorf("Name = %q", got)
	}
}
