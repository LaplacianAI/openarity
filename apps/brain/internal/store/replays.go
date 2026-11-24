package store

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ClaimReplay begins one of a proposal's tries unless its team has used its
// tries for the day, and is how many rows it began: 0 when the cap is met.
// The count and the claim hold the team's lock, so two workers cannot both
// take the last try.
func (s *Store) ClaimReplay(ctx context.Context, arg db.StartReplayParams) (int64, error) {
	var n int64
	err := s.InTx(ctx, func(q *db.Queries) error {
		if err := q.LockReplayTries(ctx, arg.TeamID); err != nil {
			return err
		}
		var err error
		n, err = q.StartReplay(ctx, arg)
		return err
	})
	return n, err
}
