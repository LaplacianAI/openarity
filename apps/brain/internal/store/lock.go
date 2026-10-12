package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (s *Store) WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (held bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to acquire a connection for the lock: %w", err)
	}
	defer conn.Release()

	q := db.New(conn)
	acquired, err := q.TryAdvisoryLock(ctx, key)
	if err != nil {
		return false, fmt.Errorf("failed to take the advisory lock: %w", err)
	}
	if !acquired {
		return false, nil
	}

	defer func() {
		unlockCtx := context.WithoutCancel(ctx)
		released, unlockErr := q.AdvisoryUnlock(unlockCtx, key)
		if unlockErr == nil && released {
			return
		}

		_ = conn.Conn().Close(unlockCtx)
		if unlockErr == nil {
			unlockErr = errors.New("the advisory lock was not held when it came to be released")
		}
		err = errors.Join(err, fmt.Errorf("failed to release the advisory lock: %w", unlockErr))
	}()

	return true, fn(ctx)
}
