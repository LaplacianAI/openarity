package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// ErrNotAllowed is a person who may not answer this request: neither one who
// decides the team's requests, nor the one it was made for, or the one it was
// made for when a rule wants someone else to answer.
var ErrNotAllowed = errors.New("you may not answer this request")

// Resumer hands a run that waited back to the workers, as its next attempt.
type Resumer interface {
	Resume(ctx context.Context, runID uuid.UUID, attempt int32) error
}

// An Answer is one person's yes or no to one request, from any door: the
// API, a reply in the chat.
type Answer struct {
	ApprovalID, TeamID, By uuid.UUID
	Approve                bool
	// Scope is once, the default, or run: this tool, any arguments, for the
	// rest of the run.
	Scope  string
	Reason string
	// Via is where it was given: dashboard, channel or cli.
	Via string
	// MayDecide is approval:decide; MaySelf is approval:self. The caller
	// reads them from the person's role as it stands now.
	MayDecide, MaySelf bool
}

const maxReason = 1000

// Decide answers a request and, once nothing of the run waits any more, hands
// the run back to the workers. The answer is written whole or not at all;
// the run is enqueued after it commits, and the sweep starts it within a
// minute if that fails. Errors are ErrNotAllowed, ErrNotPending (with
// the request as it stands), ErrExpired (marked so, and resumed), and
// pgx.ErrNoRows for a request that is not this team's.
func Decide(ctx context.Context, s TxStore, r Resumer, a Answer) (db.Approval, error) {
	scope := a.Scope
	switch scope {
	case "":
		scope = "once"
	case "once", "run":
	default:
		return db.Approval{}, fmt.Errorf("the scope is once or run, not %q", a.Scope)
	}
	status := "denied"
	if a.Approve {
		status = "approved"
	}
	var reason *string
	if said := strings.TrimSpace(a.Reason); said != "" {
		if r := []rune(said); len(r) > maxReason {
			said = string(r[:maxReason])
		}
		reason = &said
	}

	row, again, err := decideApproval(ctx, s, decision{
		ID: a.ApprovalID, TeamID: a.TeamID, By: a.By, Status: status, Scope: scope,
		Reason: reason, Via: a.Via, Allowed: func(req db.Approval) error { return mayAnswer(a, req) },
	})
	if again != nil && r != nil {
		if rerr := r.Resume(context.WithoutCancel(ctx), again.ID, again.Attempt); rerr != nil {
			// The answer stands; the sweep starts a queued run within a minute.
			err = errors.Join(err, fmt.Errorf("the answer is recorded, and run %s will start within a minute: %w", again.ID, rerr))
		}
	}
	return row, err
}

// mayAnswer is who may answer a request: anyone who decides the team's, or
// the person it was made for when they may approve their own, unless a rule
// asked for a second pair of eyes. Deciding grants nothing beyond the one
// call the agent was already allowed to make, so there is no ceiling to check.
func mayAnswer(a Answer, req db.Approval) error {
	own := req.RequestedFor != nil && *req.RequestedFor == a.By
	if !approval.MayAnswer(own, req.RequireOther, a.MayDecide, a.MaySelf) {
		return ErrNotAllowed
	}
	return nil
}
