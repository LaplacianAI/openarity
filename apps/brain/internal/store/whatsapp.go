package store

import (
	"context"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// UpdateChannelOnItsNumber is UpdateChannel for a change to a channel's
// trigger word: every account on its number is held while check reads the
// other channels' words and while the change is made, so two edits on one
// number cannot each pass a check the other is about to make false. A
// channel no number is linked to has no others, and check sees none.
func (s *Store) UpdateChannelOnItsNumber(
	ctx context.Context, arg db.UpdateChannelParams, check func(others []db.ListWhatsAppSharersRow) error,
) (db.Channel, error) {
	var out db.Channel
	err := s.InTx(ctx, func(q *db.Queries) error {
		if err := q.LockWhatsAppNumber(ctx, arg.ID); err != nil {
			return err
		}
		others, err := q.ListWhatsAppSharers(ctx, arg.ID)
		if err != nil {
			return err
		}
		if err := check(others); err != nil {
			return err
		}
		out, err = q.UpdateChannel(ctx, arg)
		return err
	})
	return out, err
}
