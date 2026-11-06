package store

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (s *Store) FinishRunWithEvent(
	ctx context.Context, finish db.FinishRunParams, event db.AppendRunEventParams,
) (int64, error) {
	var n int64
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		if n, err = q.FinishRun(ctx, finish); err != nil || n == 0 {
			return err
		}
		_, err = q.AppendRunEvent(ctx, event)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
