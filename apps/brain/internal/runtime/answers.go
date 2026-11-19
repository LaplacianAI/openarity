package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// maxAnswered caps what one approved call's result adds to the model's next
// message, as a report is capped.
const maxAnswered = 32 << 10

// catchUp is what happened while a run waited, for its model to read before
// anything else: each child that waited carries on, to its report or to
// waiting again; and each of the run's own requests that was answered is
// acted on, an approved call made then, with exactly the arguments that were
// approved. Empty when nothing happened, as on a resume after a crash.
func (e *Executor) catchUp(ctx context.Context, row db.Run, built Built, l Lineage) (string, error) {
	var parts []string

	children, err := e.store.ListWaitingChildren(ctx, row.ID)
	if err != nil {
		return "", fmt.Errorf("reading which of run %s's delegates wait: %w", row.ID, err)
	}
	for _, c := range children {
		got, err := e.adopt(ctx, c, l)
		if err != nil {
			return "", err
		}
		if got.Status == "waiting" {
			parts = append(parts, waitingReport(got))
		} else {
			parts = append(parts, report(got, reportEnd()))
		}
	}

	untold, err := e.store.ListUntoldApprovals(ctx, row.ID)
	if err != nil {
		return "", fmt.Errorf("reading what run %s's requests came to: %w", row.ID, err)
	}
	ids := make([]uuid.UUID, 0, len(untold))
	for _, a := range untold {
		said, err := e.answered(ctx, row, built, a)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("Your request to call %s (%s): %s", a.Tool, oneLine(a.Summary), said))
		ids = append(ids, a.ID)
	}
	// Told once: a crash after this and before the transcript is saved loses
	// the telling, never repeats the call, which is spent.
	if len(ids) > 0 {
		if err := e.store.MarkApprovalsTold(context.WithoutCancel(ctx), ids); err != nil {
			return "", fmt.Errorf("recording that run %s was told its answers: %w", row.ID, err)
		}
	}

	if len(parts) == 0 {
		return "", nil
	}
	return "While you waited:\n\n" + strings.Join(parts, "\n\n"), nil
}

// answered acts on one answer and says what came of it.
func (e *Executor) answered(ctx context.Context, row db.Run, built Built, a db.Approval) (string, error) {
	switch a.Status {
	case "denied":
		return declinedWith(a.Reason), nil
	case "expired":
		return expiredCall, nil
	case "cancelled":
		switch {
		case a.DecidedBy == nil:
			return cancelledCall, nil
		case row.ParentRunID != nil:
			return supersededDeep, nil
		}
		return supersededCall, nil
	case "approved":
	default:
		return "", fmt.Errorf("request %s is %s, which has no answer to act on", a.ID, a.Status)
	}

	tool, ok := built.answerable[a.Tool]
	if !ok {
		return "It was approved, but this run no longer has " + a.Tool + ", so it was not made.", nil
	}
	// Spent before it is made: a crash during the call never makes it twice.
	if _, err := e.store.ClaimApprovalUse(ctx, a.ID); errors.Is(err, pgx.ErrNoRows) {
		return usedCall, nil
	} else if err != nil {
		return "", fmt.Errorf("spending the approval for %s: %w", a.Tool, err)
	}
	result, callable, err := tool.Call(agent.WithToolCallID(ctx, a.CallID), a.Arguments)
	switch {
	case !callable:
		return "It was approved, but " + a.Tool + " cannot be called here, so it was not made.", nil
	case err != nil:
		said, _ := clip(err.Error())
		return "A person approved it, and it was made, but it failed: " + said, nil
	}
	out := result.Output
	if len(out) > maxAnswered {
		cut := maxAnswered
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "\n[the result was cut at 32 KiB]"
	}
	return "A person approved it, and it was made. Its result:\n" + out, nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
