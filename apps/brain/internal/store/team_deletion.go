package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (s *Store) TeamDeleted(ctx context.Context, id uuid.UUID) (bool, error) {
	team, err := s.GetTeam(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return team.DeletedAt != nil, nil
}

// SoftDeleteTeam marks a team deleted unless a run is queued or running in
// it, and reports how many rows it marked. The lock comes first, in its own
// statement, so a run that StartRun is writing right now is either committed
// and seen by the check, or refused by LockLiveTeam once this commits.
// No such team marks nothing.
func (s *Store) SoftDeleteTeam(ctx context.Context, id uuid.UUID) (int64, error) {
	var n int64
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockTeamForDelete(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		var err error
		n, err = q.MarkTeamDeleted(ctx, id)
		return err
	})
	return n, err
}
