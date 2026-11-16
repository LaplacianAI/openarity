package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// BindSender is ApproveSender, and then the channel user the sender spoke as
// deleted when it was their last binding: nobody can log in as one, so with
// no sender it is a member that can do nothing.
func (s *Store) BindSender(ctx context.Context, arg db.ApproveSenderParams) (db.ApproveSenderRow, error) {
	var row db.ApproveSenderRow
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		if row, err = q.ApproveSender(ctx, arg); err != nil {
			return err
		}
		return tidy(ctx, q, row.Linked > 0, arg.ExpectedHolder, arg.UserID)
	})
	return row, err
}

// BindSenderAsNew is ApproveSenderAsNew, tidied as BindSender is.
func (s *Store) BindSenderAsNew(ctx context.Context, arg db.ApproveSenderAsNewParams) (db.ApproveSenderAsNewRow, error) {
	var row db.ApproveSenderAsNewRow
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		if row, err = q.ApproveSenderAsNew(ctx, arg); err != nil {
			return err
		}
		return tidy(ctx, q, row.Linked > 0, arg.ExpectedHolder, row.UserID)
	})
	return row, err
}

// UnbindSender is RemoveSender, tidied as BindSender is.
func (s *Store) UnbindSender(ctx context.Context, arg db.RemoveSenderParams) (bool, error) {
	var removed bool
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		if removed, err = q.RemoveSender(ctx, arg); err != nil {
			return err
		}
		return tidy(ctx, q, removed, arg.ExpectedHolder, uuid.Nil)
	})
	return removed, err
}

// tidy deletes the channel user a binding was taken from, if it was their
// last. Only a binding that moved, from a holder that is not its new one.
func tidy(ctx context.Context, q *db.Queries, moved bool, from *uuid.UUID, to uuid.UUID) error {
	if !moved || from == nil || *from == to {
		return nil
	}
	if _, err := q.DeleteLostChannelUser(ctx, *from); err != nil {
		return fmt.Errorf("deleting the channel user %s left with no sender: %w", *from, err)
	}
	return nil
}

// RemoveMember takes a user out of a team, and deletes them when they are a
// channel user, who exists for that team alone.
func (s *Store) RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error {
	return s.InTx(ctx, func(q *db.Queries) error {
		if err := q.RemoveTeamMember(ctx, db.RemoveTeamMemberParams{TeamID: teamID, UserID: userID}); err != nil {
			return err
		}
		return q.DeleteChannelUser(ctx, userID)
	})
}
