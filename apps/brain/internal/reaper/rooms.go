package reaper

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// RoomDeleter deletes a room at its provider with the key it was made with.
// A room the provider no longer has is not an error.
type RoomDeleter interface {
	Delete(ctx context.Context, team uuid.UUID, provider sandbox.Provider, url string, keyID uuid.UUID, room string) error
}

type RoomRows interface {
	ExpireIdleSandboxRooms(ctx context.Context, cutoff time.Time) (int64, error)
	ClaimDeletedRooms(ctx context.Context, arg db.ClaimDeletedRoomsParams) ([]db.DeletedRoom, error)
	GetDeletedRoom(ctx context.Context, id uuid.UUID) (db.DeletedRoom, error)
	ForgetDeletedRoom(ctx context.Context, id uuid.UUID) error
	DeletedRoomBacklog(ctx context.Context) ([]db.DeletedRoomBacklogRow, error)
}

type roomEffect struct {
	rows  RoomRows
	rooms RoomDeleter
	idle  time.Duration
}

// Rooms deletes the sandbox rooms nothing owns any longer: those of a deleted
// owner, of a team that changed its provider or key, or replaced by a newer
// one. Each tombstone is the room's provider, address and the id of the key it
// was made with, so a room is deleted with the credentials that made it, and
// the key's own tombstone waits for it (ClaimDeletedSecrets).
//
// It also ends the idle: before each claim it deletes the rows of rooms unused
// for idle, which tombstones them in the same pass, so a room nobody has called
// in that long is gone at the next sweep. The next call to its owner starts a
// fresh one.
func Rooms(rows RoomRows, rooms RoomDeleter, idle time.Duration) Effect {
	return roomEffect{rows: rows, rooms: rooms, idle: idle}
}

func (roomEffect) Name() string { return "rooms" }

func (e roomEffect) Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	if _, err := e.rows.ExpireIdleSandboxRooms(ctx, time.Now().Add(-e.idle)); err != nil {
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
