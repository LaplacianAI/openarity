package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A session's stream is numbered from the session's counter, under the
// session's row lock. So everything that writes to the stream, or ends a run,
// takes that lock before it touches any run: one lock order, so two writers
// can wait on each other but never deadlock. The methods below shadow the
// generated queries of the same name, so every caller holding a *Store gets
// the lock without asking for it.

// AppendRunEvent writes one of a run's events. No row is a run this worker no
// longer holds, as before.
func (s *Store) AppendRunEvent(ctx context.Context, arg db.AppendRunEventParams) (int32, error) {
	var seq int32
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, arg.RunID); err != nil {
			return err
		}
		var err error
		seq, err = q.AppendRunEvent(ctx, arg)
		return err
	})
	return seq, err
}

// RequestRunCancel cancels a queued run at once, with every child it left
// running, and flags a running one for its worker. Each run it ends says so
// with a finished event, since no worker is there to write one.
func (s *Store) RequestRunCancel(ctx context.Context, id uuid.UUID) (db.Run, error) {
	var run db.Run
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, id); err != nil {
			return err
		}
		row, err := q.RequestRunCancel(ctx, id)
		if err != nil {
			return err
		}
		ended := row.EndedChildren
		if row.Status == "cancelled" {
			ended = append(ended, row.ID)
		}
		if err := q.AppendFinishedEvents(ctx, ended); err != nil {
			return fmt.Errorf("record the end of the runs cancelled with %s: %w", id, err)
		}
		run, err = q.GetRun(ctx, id)
		return err
	})
	return run, err
}

// ReapChildren fails a finished run's children still running, and records
// each one's end.
func (s *Store) ReapChildren(ctx context.Context, arg db.ReapChildrenParams) ([]uuid.UUID, error) {
	var reaped []uuid.UUID
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, arg.ParentID); err != nil {
			return err
		}
		var err error
		if reaped, err = q.ReapChildren(ctx, arg); err != nil {
			return err
		}
		if err := q.AppendFinishedEvents(ctx, reaped); err != nil {
			return fmt.Errorf("record the end of the children of %s: %w", arg.ParentID, err)
		}
		return nil
	})
	return reaped, err
}

// ConsumeSteer is a run taking the oldest message waiting in its session,
// when it reads a steer: steers are applied in the order they were sent, and
// only a message steers a channel's session, so the oldest pending one is the
// steer it read. A session with none, a direct one steered through the API,
// records nothing.
func (s *Store) ConsumeSteer(ctx context.Context, runID uuid.UUID) error {
	return s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, runID); err != nil {
			return err
		}
		_, err := q.ConsumeMessages(ctx, db.ConsumeMessagesParams{RunID: runID, FirstOnly: true})
		return err
	})
}

// BackfillSession numbers one session's rows from before its stream, under
// its lock. A session with nothing to number changes nothing.
func (s *Store) BackfillSession(ctx context.Context, sessionID uuid.UUID) (db.BackfillSessionSeqRow, error) {
	var done db.BackfillSessionSeqRow
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockSession(ctx, sessionID); err != nil {
			return err
		}
		var err error
		done, err = q.BackfillSessionSeq(ctx, sessionID)
		return err
	})
	return done, err
}
