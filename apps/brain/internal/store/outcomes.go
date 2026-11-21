package store

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// GiveRunFeedback records a person's word on a run, or replaces it, and a
// 👎 reopens the run's digest when it was judged good and left out of the
// sample: it is digested after all. One transaction, holding the run's
// votes, so a check recording its outcome at the same moment either sees
// the 👎 or is reopened by it.
func (s *Store) GiveRunFeedback(ctx context.Context, arg db.UpsertRunFeedbackParams) (db.RunFeedback, error) {
	var saved db.RunFeedback
	err := s.InTx(ctx, func(q *db.Queries) error {
		if err := q.LockRunVotes(ctx, arg.RunID); err != nil {
			return err
		}
		var err error
		if saved, err = q.UpsertRunFeedback(ctx, arg); err != nil {
			return err
		}
		if arg.Value == "down" {
			return q.ReopenRunDigest(ctx, arg.RunID)
		}
		return nil
	})
	return saved, err
}
