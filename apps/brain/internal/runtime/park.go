package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// waitingPayload is the waiting event: why the run waits, and what it said
// about it in its own words, for the stream and the channel to show beside
// the request.
type waitingPayload struct {
	Reason string `json:"reason"`
	Said   string `json:"said,omitempty"`
}

// What a parked run waits for: a person, or room in the team's sandbox. A run
// that waits for both waits for the person, whose part is the one to act on.
const (
	waitsForPerson = "approval"
	waitsForRoom   = "sandbox"
)

// park ends a run's attempt without finishing it: its turn ended with a
// person still to answer, or with an answer it has not been told, with
// sandbox room still to come, or with a child that waits. What it said is
// kept as its output, the transcript is saved, and it waits holding no
// worker. A root whose answers all came before it parked goes straight back
// to the queue; a delegate is left for its parent, which reads what it said
// as its report and waits in turn.
func (e *Executor) park(ctx context.Context, row db.Run, l Lineage, said, waitsFor string) error {
	ctx = context.WithoutCancel(ctx)
	payload, err := json.Marshal(waitingPayload{Reason: waitsFor, Said: said})
	if err != nil {
		return fmt.Errorf("encoding the waiting event: %w", err)
	}
	var output *string
	if said != "" {
		output = &said
	}
	parked, again, asked, err := parkAndAsk(ctx, e.store, row.ID, l.Worker, row.ParentRunID != nil, output, waitsFor, payload)
	if err != nil {
		return fmt.Errorf("parking run %s: %w", row.ID, err)
	}
	// Kicked by the waiting event, as a step is: kicked by the run, it would
	// take the run's own kick, and its answer would wait for the sweep.
	if asked != 0 && e.kickEvent != nil {
		if err := e.kickEvent(row.ID, asked); err != nil {
			e.logger.WarnContext(ctx, "prompt not kicked; the sweep will send it", "run", row.ID, "err", err)
		}
	}
	if !parked || again == nil || e.enqueue == nil {
		return nil
	}
	if err := e.enqueue(again.ID, again.Attempt); err != nil {
		e.logger.WarnContext(ctx, "run not enqueued; the sweep will start it", "run", again.ID, "err", err)
	}
	return nil
}
