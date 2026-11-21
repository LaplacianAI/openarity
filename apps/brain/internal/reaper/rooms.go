package reaper

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox/keeper"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// RoomDeleter deletes a room at its provider with the key it was made with,
// and stops an owner's room, keeping its files, while idle says it may. A room
// the provider no longer has is not an error.
type RoomDeleter interface {
	Delete(ctx context.Context, team uuid.UUID, provider sandbox.Provider, url string, keyID uuid.UUID, room string) error
	Stop(ctx context.Context, o keeper.Owner, team uuid.UUID, idle func(context.Context) (bool, error)) error
}

type RoomRows interface {
	ExpireIdleSandboxRooms(ctx context.Context, cutoff time.Time) (int64, error)
	ExpireStoppedSandboxRooms(ctx context.Context) (int64, error)
	ListIdleWorkspaceRooms(ctx context.Context, arg db.ListIdleWorkspaceRoomsParams) ([]db.SandboxRoom, error)
	SessionIsIdle(ctx context.Context, sessionID uuid.UUID) (bool, error)
	ClaimDeletedRooms(ctx context.Context, arg db.ClaimDeletedRoomsParams) ([]db.DeletedRoom, error)
	GetDeletedRoom(ctx context.Context, id uuid.UUID) (db.DeletedRoom, error)
	ForgetDeletedRoom(ctx context.Context, id uuid.UUID) error
	DeletedRoomBacklog(ctx context.Context) ([]db.DeletedRoomBacklogRow, error)
}

type roomEffect struct {
	rows   RoomRows
	rooms  RoomDeleter
	idle   time.Duration
	logger *slog.Logger
}

// stopsPerSweep is how many idle workspaces one sweep stops; the rest wait
// for the next.
const stopsPerSweep = 50

// Rooms deletes the sandbox rooms nothing owns any longer: those of a deleted
// owner, of a team that changed its provider or key, or replaced by a newer
// one. Each tombstone is the room's provider, address and the id of the key it
// was made with, so a room is deleted with the credentials that made it, and
// the key's own tombstone waits for it (ClaimDeletedSecrets).
//
// It also ends the idle, before each claim. A command server's room unused
// for idle has its row deleted, which tombstones it in the same pass; the
// next call to its owner starts a fresh one. A session's workspace is
// stopped instead, keeping its files, once unused for its team's idle
// minutes (idle when the team set none), and its row deleted once stopped
// for longer than the team's keep hours.
func Rooms(rows RoomRows, rooms RoomDeleter, idle time.Duration, logger *slog.Logger) Effect {
	if logger == nil {
		panic("reaper: a nil logger")
	}
	return roomEffect{rows: rows, rooms: rooms, idle: idle, logger: logger}
}

func (roomEffect) Name() string { return "rooms" }

func (e roomEffect) Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	if _, err := e.rows.ExpireIdleSandboxRooms(ctx, time.Now().Add(-e.idle)); err != nil {
		return nil, err
	}
	if _, err := e.rows.ExpireStoppedSandboxRooms(ctx); err != nil {
		return nil, err
	}
	if err := e.stopIdle(ctx); err != nil {
		return nil, err
	}

	rows, err := e.rows.ClaimDeletedRooms(ctx, db.ClaimDeletedRoomsParams{
		RetryBefore: &retryBefore,
		BatchSize:   batch,
	})
	if err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{Ref: row.ID.String(), TeamID: row.TeamID, Attempts: row.Attempts})
	}
	return items, nil
}

// stopIdle stops the workspaces idle past their team's minutes. One that
// cannot be stopped now is logged and tried at the next sweep; it does not
// hold back the tombstones.
func (e roomEffect) stopIdle(ctx context.Context) error {
	minutes := int32(min(e.idle/time.Minute, math.MaxInt32)) //nolint:gosec // held to MaxInt32 by min; past that is never, in practice
	rows, err := e.rows.ListIdleWorkspaceRooms(ctx, db.ListIdleWorkspaceRoomsParams{
		DefaultMinutes: minutes, Max: stopsPerSweep,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		session := row.OwnerID
		idle := func(ctx context.Context) (bool, error) { return e.rows.SessionIsIdle(ctx, session) }
		if err := e.rooms.Stop(ctx, keeper.Owner{Kind: row.OwnerKind, ID: row.OwnerID}, row.TeamID, idle); err != nil {
			e.logger.WarnContext(ctx, "an idle workspace was not stopped", "session", session, "err", err)
		}
	}
	return nil
}

func (e roomEffect) Do(ctx context.Context, item Item) (Outcome, error) {
	id, err := uuid.Parse(item.Ref)
	if err != nil {
		return 0, err
	}

	row, err := e.rows.GetDeletedRoom(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Superseded, nil
	}
	if err != nil {
		return 0, err
	}

	url := ""
	if row.ProviderUrl != nil {
		url = *row.ProviderUrl
	}
	err = e.rooms.Delete(ctx, row.TeamID, sandbox.Provider(row.Provider), url, row.SandboxSecretID, row.RoomID)
	if errors.Is(err, secrets.ErrNotFound) {
		// The key is gone, which the key's tombstone waiting for this one is
		// meant to prevent; nothing can authenticate to the provider now, so
		// retrying would only repeat this.
		return Superseded, nil
	}
	if err != nil {
		return 0, err
	}
	return Applied, nil
}

func (e roomEffect) Forget(ctx context.Context, item Item) error {
	id, err := uuid.Parse(item.Ref)
	if err != nil {
		return err
	}
	return e.rows.ForgetDeletedRoom(ctx, id)
}

func (e roomEffect) Backlog(ctx context.Context) (int64, time.Time, error) {
	rows, err := e.rows.DeletedRoomBacklog(ctx)
	if err != nil || len(rows) == 0 {
		return 0, time.Time{}, err
	}
	return rows[0].Outstanding, rows[0].Oldest, nil
}
