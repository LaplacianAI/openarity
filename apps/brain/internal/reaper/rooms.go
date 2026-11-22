package reaper

import (
	"context"
	"errors"
	"log/slog"
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
	// stopPass bounds a sweep's stops of idle workspaces, all of them.
	stopPass time.Duration
}

// stopsPerSweep is how many idle workspaces one sweep stops; the rest wait
// for the next.
const stopsPerSweep = 50

// stopTimeout bounds one provider stop within a sweep, and sweepStopTimeout
// all of them: shorter than a run's own stop, which has one room to stop.
const (
	stopTimeout      = 30 * time.Second
	sweepStopTimeout = 2 * time.Minute
)

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
	return roomEffect{rows: rows, rooms: rooms, idle: idle, logger: logger, stopPass: sweepStopTimeout}
}

func (roomEffect) Name() string { return "rooms" }

func (e roomEffect) Claim(ctx context.Context, retryBefore time.Time, batch int32) ([]Item, error) {
	if _, err := e.rows.ExpireIdleSandboxRooms(ctx, time.Now().Add(-e.idle)); err != nil {
		return nil, err
	}
	if _, err := e.rows.ExpireStoppedSandboxRooms(ctx); err != nil {
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

// Prepare stops the workspaces idle past their team's minutes, once a sweep
// rather than once a batch of tombstones, so a provider that is down costs
// the sweep one pass's time. One that cannot be stopped now is logged and
// tried at the next sweep; it does not hold back the tombstones.
func (e roomEffect) Prepare(ctx context.Context) error {
	rows, err := e.rows.ListIdleWorkspaceRooms(ctx, db.ListIdleWorkspaceRoomsParams{
		DefaultMinutes: idleMinutes(e.idle), Max: stopsPerSweep,
	})
	if err != nil {
		return err
	}
	// A time for the whole pass, so a provider that is down costs the
	// tombstones behind it minutes at most; the rest wait for the next sweep.
	pass, cancel := context.WithTimeout(ctx, e.stopPass)
	defer cancel()
	failed, asked := 0, 0
	for _, row := range rows {
		if pass.Err() != nil {
			break
		}
		asked++
		if !e.stopOne(pass, row) {
			failed++
		}
	}
	if asked > 0 {
		e.logger.InfoContext(ctx, "idle workspaces asked to stop", "asked", asked, "failed", failed, "listed", len(rows))
	}
	return nil
}

// stopOne stops one idle workspace, within a time of its own so one slow
// provider does not hold up the rest of the sweep.
func (e roomEffect) stopOne(ctx context.Context, row db.SandboxRoom) bool {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	session := row.OwnerID
	idle := func(ctx context.Context) (bool, error) { return e.rows.SessionIsIdle(ctx, session) }
	if err := e.rooms.Stop(ctx, keeper.Owner{Kind: row.OwnerKind, ID: row.OwnerID}, row.TeamID, idle); err != nil {
		e.logger.WarnContext(ctx, "an idle workspace was not stopped", "session", session, "err", err)
		return false
	}
	return true
}

// idleMinutes is the deployment's idle time in whole minutes, rounded up and
// at least one: a time under a minute is not a reason to stop every room at
// once.
func idleMinutes(d time.Duration) int32 {
	m := d / time.Minute
	if d%time.Minute != 0 {
		m++
	}
	if m < 1 {
		return 1
	}
	// The longest Duration is 153,722,868 minutes, well inside an int32.
	return int32(m) //nolint:gosec // see above
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
