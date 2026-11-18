package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// WithRoomLock runs fn holding the lock for one room owner, so concurrent
// callers, in this brain and in others, take turns. It waits for the lock
// rather than trying it, because the caller behind it wants the room the
// first one is making. The lock is a session lock on one pooled connection,
// held for as long as fn runs: a cold start is up to the start timeout, which
// is long to hold a connection. Callers waiting behind it each hold one too,
// so a burst of calls to one cold server is bounded by the pool, not by the
// number of callers.
//
// A caller whose context ends while it waits gets the context's error and
// never ran fn.
func (s *Store) WithRoomLock(ctx context.Context, kind string, owner uuid.UUID, fn func(context.Context) error) (err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire a connection for the room lock: %w", err)
	}
	defer conn.Release()

	q := db.New(conn)
	args := db.LockSandboxRoomParams{OwnerKind: kind, OwnerID: owner.String()}
	if err := q.LockSandboxRoom(ctx, args); err != nil {
		// A wait that was cancelled may have been granted on the server
		// meanwhile; closing the connection releases whatever it holds.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		return fmt.Errorf("failed to take the room lock: %w", err)
	}

	defer func() {
		unlockCtx := context.WithoutCancel(ctx)
		released, unlockErr := q.UnlockSandboxRoom(unlockCtx, db.UnlockSandboxRoomParams{OwnerKind: kind, OwnerID: owner.String()})
		if unlockErr == nil && released {
			return
		}

		_ = conn.Conn().Close(unlockCtx)
		if unlockErr == nil {
			unlockErr = errors.New("the room lock was not held when it came to be released")
		}
		err = errors.Join(err, fmt.Errorf("failed to release the room lock: %w", unlockErr))
	}()

	return fn(ctx)
}

// ReplaceSandboxRoom saves a new room for its owner in place of its old one,
// in one transaction: the old row's deletion tombstones its room and its
// token, and the new row exists only if that did. old is the row being
// replaced, or nil when the owner had none.
func (s *Store) ReplaceSandboxRoom(ctx context.Context, old *db.SandboxRoom, arg db.CreateSandboxRoomParams) (db.SandboxRoom, error) {
	var out db.SandboxRoom
	err := s.InTx(ctx, func(q *db.Queries) error {
		if old != nil {
			if err := q.DeleteSandboxRoom(ctx, old.ID); err != nil {
				return err
			}
		}
		row, err := q.CreateSandboxRoom(ctx, arg)
		out = row
		return err
	})
	return out, err
}
