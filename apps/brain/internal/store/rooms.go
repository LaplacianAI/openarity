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
func (s *Store) WithRoomLock(ctx context.Context, kind string, owner uuid.UUID, fn func(context.Context) error) error {
	args := db.LockSandboxRoomParams{OwnerKind: kind, OwnerID: owner.String()}
	return s.withSessionLock(ctx, "room",
		func(q *db.Queries) error { return q.LockSandboxRoom(ctx, args) },
		func(ctx context.Context, q *db.Queries) (bool, error) {
			return q.UnlockSandboxRoom(ctx, db.UnlockSandboxRoomParams(args))
		}, fn)
}

// withSessionLock runs fn holding a session advisory lock on one pooled
// connection: lock takes it, waiting, and unlock gives it back. A lock that
// cannot be given back closes the connection, which releases it.
func (s *Store) withSessionLock(
	ctx context.Context, what string,
	lock func(*db.Queries) error, unlock func(context.Context, *db.Queries) (bool, error),
	fn func(context.Context) error,
) (err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire a connection for the %s lock: %w", what, err)
	}
	defer conn.Release()

	q := db.New(conn)
	if err := lock(q); err != nil {
		// A wait that was cancelled may have been granted on the server
		// meanwhile; closing the connection releases whatever it holds.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		return fmt.Errorf("failed to take the %s lock: %w", what, err)
	}

	defer func() {
		unlockCtx := context.WithoutCancel(ctx)
		released, unlockErr := unlock(unlockCtx, q)
		if unlockErr == nil && released {
			return
		}

		_ = conn.Conn().Close(unlockCtx)
		if unlockErr == nil {
			unlockErr = fmt.Errorf("the %s lock was not held when it came to be released", what)
		}
		err = errors.Join(err, fmt.Errorf("failed to release the %s lock: %w", what, unlockErr))
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
