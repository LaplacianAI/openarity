package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// roomFixture is a team with a sandbox, a command server, and a room for it.
type roomFixture struct {
	team   db.Team
	key    uuid.UUID
	server db.McpServer
	room   db.SandboxRoom
}

func newRoomFixture(t *testing.T, s *Store, name string) roomFixture {
	t.Helper()

	f := roomFixture{team: mustCreate(t, s, name), key: uuid.New()}
	row := settings(f.team.ID)
	row.SandboxProvider, row.SandboxSecretID, row.SandboxUrl = "sandboxd", &f.key, ptrTo("https://sbx.example")
	mustUpsertSettings(t, s, row)

	f.server = mustCreateServer(t, s, db.CreateMCPServerParams{
		TeamID: f.team.ID, Name: "mail", Command: []string{"npx", "mail"}, Env: []byte(`{}`),
	})
	f.room = f.newRoom(t, s, f.server.ID)
	return f
}

func (f roomFixture) newRoom(t *testing.T, s *Store, owner uuid.UUID) db.SandboxRoom {
	t.Helper()

	room, err := s.CreateSandboxRoom(t.Context(), db.CreateSandboxRoomParams{
		ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "mcp_server", OwnerID: owner,
		Provider: "sandboxd", ProviderUrl: ptrTo("https://sbx.example"), RoomID: "room-" + owner.String(),
		SpecHash: "hash", SandboxSecretID: f.key,
	})
	if err != nil {
		t.Fatalf("CreateSandboxRoom: %v", err)
	}
	return room
}

func deletedRooms(t *testing.T, s *Store) []db.DeletedRoom {
	t.Helper()

	rows, err := s.ClaimDeletedRooms(t.Context(), db.ClaimDeletedRoomsParams{RetryBefore: at(time.Now()), BatchSize: 100})
	if err != nil {
		t.Fatalf("ClaimDeletedRooms: %v", err)
	}
	return rows
}

// A room's row going is what records the room and its token for the sweep, in
// the spelling secrets.RoomPath builds, and with the key's id: the sweep needs
// both after the row is gone.
func TestARoomDeletedLeavesItsRoomAndItsTokenToSweep(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-deleted")

	if err := s.DeleteSandboxRoom(t.Context(), f.room.ID); err != nil {
		t.Fatalf("DeleteSandboxRoom: %v", err)
	}

	rooms := deletedRooms(t, s)
	if len(rooms) != 1 || rooms[0].RoomID != f.room.RoomID || rooms[0].Provider != "sandboxd" ||
		rooms[0].SandboxSecretID != f.key || rooms[0].TeamID != f.team.ID ||
		rooms[0].ProviderUrl == nil || *rooms[0].ProviderUrl != "https://sbx.example" {
		t.Errorf("tombstones = %+v, want the room, its provider, address and key", rooms)
	}
	if got, want := secretPaths(t, s), []string{secrets.RoomPath(f.team.ID, f.room.ID)}; !samePaths(got, want) {
		t.Errorf("secret tombstones = %v, want %v", got, want)
	}
}

// A command server deleted takes its room's row with it, and the room is
// recorded for deletion. The owner is polymorphic so no foreign key does this;
// the trigger on the owner's own table does.
func TestDeletingTheServerTombstonesItsRoom(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-server")
	other := mustCreateServer(t, s, db.CreateMCPServerParams{
		TeamID: f.team.ID, Name: "files", Command: []string{"npx", "files"}, Env: []byte(`{}`),
	})
	kept := f.newRoom(t, s, other.ID)

	if err := s.DeleteMCPServer(t.Context(), f.server.ID); err != nil {
		t.Fatalf("DeleteMCPServer: %v", err)
	}

	if _, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: f.server.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("the deleted server's room row: %v, want none", err)
	}
	if _, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: other.ID}); err != nil {
		t.Errorf("another server's room row: %v, want it kept", err)
	}
	if rooms := deletedRooms(t, s); len(rooms) != 1 || rooms[0].RoomID != f.room.RoomID {
		t.Errorf("tombstones = %+v, want only the deleted server's room, not %s", rooms, kept.RoomID)
	}
	if paths := secretPaths(t, s); !slices.Contains(paths, secrets.RoomPath(f.team.ID, f.room.ID)) {
		t.Errorf("secret tombstones = %v, want the room's token", paths)
	}
}

// A team deleted takes its servers and their rooms, and still leaves the
// rooms to delete: the tombstone does not depend on the team.
func TestDeletingTheTeamTombstonesItsRooms(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-team")

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM teams WHERE id = $1`, f.team.ID); err != nil {
		t.Fatalf("deleting the team: %v", err)
	}

	if rooms := deletedRooms(t, s); len(rooms) != 1 || rooms[0].TeamID != f.team.ID || rooms[0].SandboxSecretID != f.key {
		t.Errorf("tombstones = %+v, want the team's room", rooms)
	}
	if n := count(t, s, `SELECT count(*) FROM sandbox_rooms`); n != 0 {
		t.Errorf("%d room rows remain", n)
	}
}

// A team that changes its provider, its address or its key no longer owns the
// rooms made under the old ones. Saving the same sandbox again, or only a
// template, leaves them.
func TestChangingTheSandboxTombstonesTheTeamsRooms(t *testing.T) {
	t.Parallel()

	same := func(p *db.UpsertTeamSettingsParams) {}
	for name, tc := range map[string]struct {
		change func(*db.UpsertTeamSettingsParams, uuid.UUID)
		gone   bool
	}{
		"saved again":     {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) { same(p) }, false},
		"another key":     {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) { p.SandboxSecretID = ptrTo(uuid.New()) }, true},
		"another address": {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) { p.SandboxUrl = ptrTo("https://other.example") }, true},
		"another provider": {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) {
			p.SandboxProvider, p.SandboxUrl = "daytona", nil
		}, true},
		"turned off": {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) {
			p.SandboxProvider, p.SandboxUrl, p.SandboxSecretID = "none", nil, nil
		}, true},
		"a search change": {func(p *db.UpsertTeamSettingsParams, _ uuid.UUID) { p.Learning = true }, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := queryStore(t)
			f := newRoomFixture(t, s, "rooms-settings")
			unrelated := newRoomFixture(t, s, "rooms-unrelated")

			p := settings(f.team.ID)
			p.SandboxProvider, p.SandboxSecretID, p.SandboxUrl = "sandboxd", &f.key, ptrTo("https://sbx.example")
			tc.change(&p, f.key)
			mustUpsertSettings(t, s, p)

			rooms := deletedRooms(t, s)
			if tc.gone {
				if len(rooms) != 1 || rooms[0].RoomID != f.room.RoomID || rooms[0].SandboxSecretID != f.key {
					t.Errorf("tombstones = %+v, want the team's room with the key it was made with", rooms)
				}
			} else if len(rooms) != 0 {
				t.Errorf("tombstones = %+v, want none", rooms)
			}
			if n := count(t, s, `SELECT count(*) FROM sandbox_rooms WHERE team_id = $1`, unrelated.team.ID); n != 1 {
				t.Errorf("another team lost its room: %d rows", n)
			}
		})
	}
}

// The key a room was made with is destroyed only when no room needs it: a room
// would be left at a provider nothing can authenticate to. A key whose team
// replaced it waits for the live rooms, then for the rooms owed a deletion, and
// is claimed when both are gone.
func TestAKeyIsNotClaimedWhileARoomStillNeedsIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-key")
	keyPath := secrets.Path(f.team.ID, secrets.KindSandbox, f.key)
	// Recorded as a replaced key is, while the room row still names it.
	if err := s.RecordDeletedSecret(t.Context(), db.RecordDeletedSecretParams{Path: keyPath, TeamID: f.team.ID}); err != nil {
		t.Fatal(err)
	}

	if got := secretPaths(t, s); slices.Contains(got, keyPath) {
		t.Fatalf("the key was claimed while a live room used it: %v", got)
	}

	if err := s.DeleteSandboxRoom(t.Context(), f.room.ID); err != nil {
		t.Fatal(err)
	}
	if got := secretPaths(t, s); slices.Contains(got, keyPath) {
		t.Fatalf("the key was claimed while the room's deletion was owed: %v", got)
	}

	rooms := deletedRooms(t, s)
	if len(rooms) != 1 {
		t.Fatalf("tombstones = %+v", rooms)
	}
	if err := s.ForgetDeletedRoom(t.Context(), rooms[0].ID); err != nil {
		t.Fatal(err)
	}
	// Claiming leased the key's tombstone the first time it was offered; a
	// claim past the lease asks again.
	later, err := s.ClaimDeletedSecrets(t.Context(), db.ClaimDeletedSecretsParams{RetryBefore: at(time.Now().Add(time.Minute)), BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range later {
		paths = append(paths, r.Path)
	}
	if !slices.Contains(paths, keyPath) {
		t.Errorf("claimed %v, want the key once its rooms are gone", paths)
	}
}

// A key is waited for by the room it made and by no other: a key nothing
// used, and a key another team's room used, are claimed as before.
func TestAKeyNoRoomUsesIsClaimedAtOnce(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-other-key")
	unused := secrets.Path(f.team.ID, secrets.KindSandbox, uuid.New())
	if err := s.RecordDeletedSecret(t.Context(), db.RecordDeletedSecretParams{Path: unused, TeamID: f.team.ID}); err != nil {
		t.Fatal(err)
	}

	if got := secretPaths(t, s); !slices.Contains(got, unused) {
		t.Errorf("claimed %v, want a key no room uses", got)
	}
}

// A room's own token is not a key, and the wait does not hold it: it is
// claimed as soon as the row is gone.
func TestARoomTokenIsNotHeldByTheKeyWait(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-token")
	if err := s.DeleteSandboxRoom(t.Context(), f.room.ID); err != nil {
		t.Fatal(err)
	}

	if got := secretPaths(t, s); !slices.Contains(got, secrets.RoomPath(f.team.ID, f.room.ID)) {
		t.Errorf("claimed %v, want the room's token", got)
	}
}

// Only a room unused since the cutoff is expired, and one whose owner is being
// ensured is skipped, not waited for.
func TestIdleRoomsExpireAndARoomInUseDoesNot(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	idle := newRoomFixture(t, s, "rooms-idle")
	busy := newRoomFixture(t, s, "rooms-busy")
	fresh := newRoomFixture(t, s, "rooms-fresh")
	old := time.Now().Add(-time.Hour)
	for _, f := range []roomFixture{idle, busy} {
		if _, err := s.pool.Exec(t.Context(), `UPDATE sandbox_rooms SET last_used_at = $2 WHERE id = $1`, f.room.ID, old); err != nil {
			t.Fatal(err)
		}
	}

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.WithRoomLock(t.Context(), "mcp_server", busy.server.ID, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	n, err := s.ExpireIdleSandboxRooms(t.Context(), time.Now().Add(-30*time.Minute))
	close(release)
	if lockErr := <-done; lockErr != nil {
		t.Fatalf("WithRoomLock: %v", lockErr)
	}
	if err != nil {
		t.Fatalf("ExpireIdleSandboxRooms: %v", err)
	}

	if n != 1 {
		t.Errorf("expired %d rooms, want 1", n)
	}
	rooms := deletedRooms(t, s)
	if len(rooms) != 1 || rooms[0].RoomID != idle.room.RoomID {
		t.Errorf("tombstones = %+v, want the idle room only", rooms)
	}
	for name, f := range map[string]roomFixture{"busy": busy, "fresh": fresh} {
		if _, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: f.server.ID}); err != nil {
			t.Errorf("the %s room: %v, want it kept", name, err)
		}
	}
}

// The lock is held by one caller at a time per owner, waits rather than tries,
// and is released when the function returns, with an error or without.
func TestTheRoomLockIsHeldByOneCallerPerOwner(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	owner, other := uuid.New(), uuid.New()

	held, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- s.WithRoomLock(t.Context(), "mcp_server", owner, func(context.Context) error {
			close(held)
			<-release
			return errors.New("the first failed")
		})
	}()
	<-held

	// Another owner is not held up.
	if err := s.WithRoomLock(t.Context(), "mcp_server", other, func(context.Context) error { return nil }); err != nil {
		t.Errorf("another owner's lock: %v", err)
	}

	// The same owner waits, and a caller that gives up never runs.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	ran := false
	err := s.WithRoomLock(ctx, "mcp_server", owner, func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Errorf("a waiting caller: err = %v, ran = %v, want its deadline and no run", err, ran)
	}

	// It is released when the first returns, though it returned an error.
	second := make(chan error, 1)
	go func() {
		second <- s.WithRoomLock(t.Context(), "mcp_server", owner, func(context.Context) error { return nil })
	}()
	select {
	case err := <-second:
		t.Fatalf("the second ran while the first held the lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-first; err == nil || err.Error() != "the first failed" {
		t.Errorf("first = %v, want its own error back", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Errorf("second: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not released when the first returned")
	}
	if n := s.pool.Stat().AcquiredConns(); n != 0 {
		t.Errorf("%d connections still held", n)
	}
}

// A replacement is one transaction: when the new row cannot be written the old
// one stays, and nothing is tombstoned.
func TestAFailedReplacementKeepsTheOldRoom(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-replace")

	_, err := s.ReplaceSandboxRoom(t.Context(), &f.room, db.CreateSandboxRoomParams{
		ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "mcp_server", OwnerID: f.server.ID,
		Provider: "modal", RoomID: "r", SpecHash: "h", SandboxSecretID: f.key,
	})

	if err == nil || constraintName(err) != "sandbox_rooms_provider_known" {
		t.Fatalf("err = %v, want the provider check", err)
	}
	if got, err := s.GetSandboxRoom(t.Context(), db.GetSandboxRoomParams{OwnerKind: "mcp_server", OwnerID: f.server.ID}); err != nil || got.ID != f.room.ID {
		t.Errorf("room = %+v, %v, want the old one kept", got, err)
	}
	if rooms := deletedRooms(t, s); len(rooms) != 0 {
		t.Errorf("tombstones = %+v, want none from a rolled-back replacement", rooms)
	}
}

// A replacement tombstones what it replaced and saves the new room.
func TestAReplacementTombstonesTheOldRoom(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-replaced")

	next, err := s.ReplaceSandboxRoom(t.Context(), &f.room, db.CreateSandboxRoomParams{
		ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "mcp_server", OwnerID: f.server.ID,
		Provider: "sandboxd", RoomID: "new", SpecHash: "h2", SandboxSecretID: f.key,
	})
	if err != nil {
		t.Fatalf("ReplaceSandboxRoom: %v", err)
	}

	if next.RoomID != "new" || next.ID == f.room.ID {
		t.Errorf("room = %+v", next)
	}
	if rooms := deletedRooms(t, s); len(rooms) != 1 || rooms[0].RoomID != f.room.RoomID {
		t.Errorf("tombstones = %+v, want the old room", rooms)
	}
	if paths := secretPaths(t, s); !slices.Contains(paths, secrets.RoomPath(f.team.ID, f.room.ID)) || slices.Contains(paths, secrets.RoomPath(f.team.ID, next.ID)) {
		t.Errorf("secret tombstones = %v, want the old token and not the new", paths)
	}
}

// One room per owner, of a kind the table knows, at a provider it knows.
func TestARoomThatCannotBeIsRefused(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	f := newRoomFixture(t, s, "rooms-refused")

	for name, tc := range map[string]struct {
		change func(*db.CreateSandboxRoomParams)
		want   string
	}{
		"a second room for the owner": {func(p *db.CreateSandboxRoomParams) {}, "sandbox_rooms_one_per_owner"},
		"an unknown owner kind": {func(p *db.CreateSandboxRoomParams) {
			p.OwnerKind, p.OwnerID = "agent", uuid.New()
		}, "sandbox_rooms_owner_kind_known"},
		"an unknown provider": {func(p *db.CreateSandboxRoomParams) {
			p.Provider, p.OwnerID = "modal", uuid.New()
		}, "sandbox_rooms_provider_known"},
		"no room id": {func(p *db.CreateSandboxRoomParams) {
			p.RoomID, p.OwnerID = "", uuid.New()
		}, "sandbox_rooms_room_present"},
	} {
		p := db.CreateSandboxRoomParams{
			ID: uuid.New(), TeamID: f.team.ID, OwnerKind: "mcp_server", OwnerID: f.server.ID,
			Provider: "sandboxd", RoomID: "r", SpecHash: "h", SandboxSecretID: f.key,
		}
		tc.change(&p)
		_, err := s.CreateSandboxRoom(t.Context(), p)
		if err == nil || constraintName(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}
}

// Rolling back drops the tables, and tombstones the tokens of rooms still
// there so none is left that nothing names; migrating again works.
func TestSandboxRoomsRollBack(t *testing.T) {
	s := migrationStore(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	f := newRoomFixture(t, s, "rooms-down")

	downTo(t, s, beforeSandboxRooms)

	if n := count(t, s, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('sandbox_rooms', 'deleted_rooms')`); n != 0 {
		t.Errorf("rolled back, %d room tables are still there", n)
	}
	if !slices.Contains(rawSecretPaths(t, s), secrets.RoomPath(f.team.ID, f.room.ID)) {
		t.Error("rolled back, the room's token has no tombstone")
	}
	if n := count(t, s, `SELECT count(*) FROM pg_trigger WHERE tgname IN
		('sandbox_rooms_tombstone_deleted', 'mcp_servers_tombstone_rooms', 'team_settings_tombstone_rooms')`); n != 0 {
		t.Errorf("rolled back, %d room triggers are still there", n)
	}
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate after rolling back: %v", err)
	}
}

const beforeSandboxRooms = 20261118150000

// rawSecretPaths reads the tombstones directly. Claiming them is a query of
// the newest schema, which a rolled-back one cannot run: it names the room
// tables.
func rawSecretPaths(t *testing.T, s *Store) []string {
	t.Helper()

	rows, err := s.pool.Query(t.Context(), `SELECT path FROM deleted_secrets`)
	if err != nil {
		t.Fatalf("reading tombstones: %v", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}
