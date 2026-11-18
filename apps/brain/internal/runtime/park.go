package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type waitingPayload struct {
	Reason string `json:"reason"`
}

// park ends a run's attempt without finishing the run: a tool asked a person,
// the transcript is saved, and the run waits holding no worker. A decision
// queues it again; one that landed before the park did is seen here, and the
// run goes straight back to the queue.
func (e *Executor) park(ctx context.Context, row db.Run, l Lineage) error {
	ctx = context.WithoutCancel(ctx)
	payload, err := json.Marshal(waitingPayload{Reason: "approval"})
	if err != nil {
		return fmt.Errorf("encoding the waiting event: %w", err)
	}
	parked, again, err := parkRun(ctx, e.store, row.ID, l.Worker, payload)
	if err != nil {
		return fmt.Errorf("parking run %s: %w", row.ID, err)
	}
	if !parked || again == nil || e.enqueue == nil {
		return nil
	}
	if err := e.enqueue(again.ID, again.Attempt); err != nil {
		e.logger.WarnContext(ctx, "run not enqueued; the sweep will start it", "run", again.ID, "err", err)
	}
	return nil
}
