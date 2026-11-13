package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// backfillPage is how many sessions are read at a time. Each is numbered in a
// transaction of its own, so a page is only how often progress is logged.
const backfillPage = 500

// backfillSessionSeq numbers every session's rows from before it had a
// stream, so a stream opened from the start replays the whole conversation.
// Safe to stop at any point and run again: a session is numbered whole or
// not at all, and one already numbered is left as it is.
func backfillSessionSeq(ctx context.Context, logger *slog.Logger, dbStore *store.Store) error {
	var sessions, messages, events int64
	after := uuid.Nil
	for {
		ids, err := dbStore.ListSessionIDsAfter(ctx, db.ListSessionIDsAfterParams{After: after, PageSize: backfillPage})
		if err != nil {
			return fmt.Errorf("listing the sessions after %s: %w", after, err)
		}
		for _, id := range ids {
			done, err := dbStore.BackfillSession(ctx, id)
			if err != nil {
				return fmt.Errorf("numbering session %s: %w", id, err)
			}
			sessions++
			messages += done.Messages
			events += done.Events
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		logger.InfoContext(ctx, "Sessions numbered", "sessions", sessions, "messages", messages, "events", events)
		if len(ids) < backfillPage {
			break
		}
	}
	logger.InfoContext(ctx, "Backfill finished", "sessions", sessions, "messages", messages, "events", events)
	return nil
}
