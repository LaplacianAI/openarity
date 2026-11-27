package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// WaitForRoom opens a run's wait for sandbox room, its session held first as
// every writer of a run takes it, so a cancel or a person writing again,
// which end the tree's waits under the same lock, either comes before (and
// no wait opens, as the run no longer runs or its wait is ended) or after
// (and ends this one). pgx.ErrNoRows is no wait opened.
func (s *Store) WaitForRoom(ctx context.Context, run uuid.UUID, reason string, firstSeconds float64) (db.RoomWait, error) {
	var w db.RoomWait
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, run); err != nil {
			return fmt.Errorf("lock the session of run %s: %w", run, err)
		}
		var err error
		w, err = q.OpenRoomWait(ctx, db.OpenRoomWaitParams{RunID: run, Reason: reason, FirstSeconds: firstSeconds})
		return err
	})
	return w, err
}
