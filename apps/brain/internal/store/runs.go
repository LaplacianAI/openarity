package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// Apology is what goes back to a conversation when its run fails. Never the
// error itself: that can name a model endpoint, a server or a setting.
const Apology = "Sorry, the team couldn't answer that just now. Please try again in a little while."

// FinishRunWithEvent ends a run and writes its finished event in one
// transaction. A run answering a channel conversation does more in that
// transaction, holding the session's lock first, as delivery does, so a
// message can neither steer it after this has looked nor start a second run
// beside the one this starts:
//
//   - the steers that came after its last model request are taken, and one run
//     is started to answer them, returned for the caller to hand to the workers;
//   - the reply goes in the outbox: its answer, or the apology if it failed.
//     A cancelled run sends nothing; someone stopped it on purpose.
func (s *Store) FinishRunWithEvent(
	ctx context.Context, finish db.FinishRunParams, event db.AppendRunEventParams,
) (int64, uuid.UUID, error) {
	var n int64
	var followUp uuid.UUID
	err := s.InTx(ctx, func(q *db.Queries) error {
		run, err := q.GetRun(ctx, finish.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read run %s: %w", finish.ID, err)
		}
		if run.ReplyToChannel {
			if _, err := q.LockSession(ctx, run.SessionID); err != nil {
				return fmt.Errorf("lock session %s: %w", run.SessionID, err)
			}
		}

		if n, err = q.FinishRun(ctx, finish); err != nil || n == 0 {
			return err
		}
		if _, err = q.AppendRunEvent(ctx, event); err != nil {
			return err
		}
		if !run.ReplyToChannel || finish.Status == "cancelled" {
			return nil
		}
		followUp, err = answerChannel(ctx, q, run, finish)
		return err
	})
	if err != nil {
		return 0, uuid.Nil, err
	}
	return n, followUp, nil
}

func answerChannel(ctx context.Context, q *db.Queries, run db.Run, finish db.FinishRunParams) (uuid.UUID, error) {
	left, err := q.TakeLeftoverSteers(ctx, run.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("take the steers run %s never read: %w", run.ID, err)
	}

	var followUp uuid.UUID
	if len(left) > 0 {
		followUp = uuid.New()
		if _, err := q.CreateChannelRun(ctx, db.CreateChannelRunParams{
			ID: followUp, TeamID: run.TeamID, SessionID: run.SessionID, UserID: run.UserID,
			Input: strings.Join(left, "\n"),
		}); err != nil {
			return uuid.Nil, fmt.Errorf("start a run for what came after run %s: %w", run.ID, err)
		}
	}

	kind, text := "apology", Apology
	if finish.Status == "succeeded" && finish.Output != nil && strings.TrimSpace(*finish.Output) != "" {
		kind, text = "answer", *finish.Output
	}
	// No row is a session whose channel is gone: nowhere to send it.
	if _, err := q.InsertReply(ctx, db.InsertReplyParams{RunID: run.ID, Kind: kind, Text: text}); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("write the reply to run %s: %w", run.ID, err)
	}
	return followUp, nil
}
