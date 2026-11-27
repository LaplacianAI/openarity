package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/approval"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

var (
	// ErrNotPending is a request already answered, expired or cancelled.
	ErrNotPending = errors.New("the request is no longer waiting for an answer")
	// ErrExpired is a request answered after its time: it is marked expired
	// rather than decided, and its run is resumed with that.
	ErrExpired = errors.New("the request expired before it was answered")
)

// Resumed is a run queued again because nothing of its tree waits any more:
// the caller enqueues it, as this attempt, once the transaction commits.
type Resumed = db.RequeueAnsweredRow

// TxStore runs a function in one transaction; *store.Store is one.
type TxStore interface {
	InTx(ctx context.Context, fn func(*db.Queries) error) error
}

// parkAndAsk parks a run whose turn ended waiting: its waiting event, then
// the run set waiting with no worker, what it said as its output. A decision
// can land before the run has parked; if nothing of the tree is pending any
// more, the root is queued again in the same transaction rather than left
// waiting for an answer already given. A delegate is parked the same way and
// never queued: its parent's call takes it up again, and the root is what is
// queued. Parked is false when this worker no longer held the run.
//
// A top-level run that now waits asks its conversation in the same
// transaction: asked is the waiting event's seq when a prompt was queued,
// which names it to the reply sender, and 0 when none was. One that waits
// for nobody, only for sandbox room, tells its conversation what it said.
func parkAndAsk(ctx context.Context, s TxStore, runID uuid.UUID, worker string, child bool, said *string, waitsFor string, payload []byte) (parked bool, again *Resumed, asked int32, err error) {
	err = s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, runID); err != nil {
			return fmt.Errorf("lock the session of run %s: %w", runID, err)
		}
		seq, err := q.AppendRunEvent(ctx, db.AppendRunEventParams{
			RunID: runID, Worker: worker, Kind: "waiting", Payload: payload,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no longer ours
		} else if err != nil {
			return fmt.Errorf("record that run %s is waiting: %w", runID, err)
		}
		var n int64
		if child {
			n, err = q.ParkChild(ctx, db.ParkChildParams{ID: runID, Worker: worker, Said: said, WaitingFor: waitsFor})
		} else {
			n, err = q.ParkRun(ctx, db.ParkRunParams{ID: runID, Worker: worker, Said: said, WaitingFor: waitsFor})
		}
		if err != nil || n == 0 {
			return err
		}
		parked = true
		if child {
			return nil
		}
		row, err := q.RequeueAnswered(ctx, runID)
		switch {
		case err == nil:
			again = &row
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("queue run %s again: %w", runID, err)
		}
		text := ""
		if said != nil {
			text = *said
		}
		ok, err := ask(ctx, q, runID, seq, text)
		if ok {
			asked = seq
		}
		return err
	})
	return parked, again, asked, err
}

// ask queues the prompt a waiting top-level run's conversation is sent: what
// the run said, then the brain's footer for each request its tree still
// asks. Its person is asked to answer in the chat only where they may approve
// their own; otherwise the chat is told an approver was asked. A run that
// answers no channel inserts nothing.
func ask(ctx context.Context, q *db.Queries, runID uuid.UUID, seq int32, said string) (bool, error) {
	pending, err := q.ListTreePending(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("list what run %s asks: %w", runID, err)
	}
	// Empty only when what the tree waits for is sandbox room: a run that has
	// just parked goes back to the queue when nothing of its tree is pending
	// and no room is still waited for.
	if len(pending) == 0 {
		return tellWaiting(ctx, q, runID, seq, said)
	}
	maySelf := map[uuid.UUID]bool{}
	reqs := make([]approval.PromptRequest, len(pending))
	for i, a := range pending {
		self := false
		if a.RequestedFor != nil {
			may, seen := maySelf[*a.RequestedFor]
			if !seen {
				if may, err = q.MemberMay(ctx, db.MemberMayParams{
					TeamID: a.TeamID, UserID: *a.RequestedFor, Action: "approval:self",
				}); err != nil {
					return false, fmt.Errorf("may %s approve their own: %w", *a.RequestedFor, err)
				}
				maySelf[*a.RequestedFor] = may
			}
			self = may
		}
		r := approval.PromptRequest{
			ID: a.ID.String(), Tool: a.Tool, Summary: a.Summary, ExpiresAt: a.ExpiresAt,
			Ask: approval.AsksInChat(a.RequireOther, self),
		}
		if r.Ask {
			r.Code = a.Code
		}
		reqs[i] = r
	}
	detail, err := json.Marshal(approval.Prompt{Requests: reqs})
	if err != nil {
		return false, fmt.Errorf("encode the prompt: %w", err)
	}
	n, err := q.InsertApprovalPrompt(ctx, db.InsertApprovalPromptParams{
		RunID: runID, EventSeq: seq, Text: approval.PromptText(said, reqs, time.Now()), Detail: detail,
	})
	if err != nil {
		return false, fmt.Errorf("ask run %s's conversation: %w", runID, err)
	}
	return n > 0, nil
}

// tellWaiting sends a run's conversation what it said as it parked to wait
// for sandbox room, or, when it said nothing, that it waits.
func tellWaiting(ctx context.Context, q *db.Queries, runID uuid.UUID, seq int32, said string) (bool, error) {
	if strings.TrimSpace(said) == "" {
		said = waitingQuietly
	}
	n, err := q.InsertWaitingReply(ctx, db.InsertWaitingReplyParams{RunID: runID, EventSeq: seq, Text: said})
	if err != nil {
		return false, fmt.Errorf("tell run %s's conversation it waits: %w", runID, err)
	}
	return n > 0, nil
}

// A decision is one person's answer to one request, as it is written.
type decision struct {
	ID, TeamID uuid.UUID
	By         uuid.UUID
	// Status is approved or denied.
	Status string
	// Scope is once or run.
	Scope  string
	Reason *string
	Via    string
	// Allowed is whether By may answer this request, asked once the row is
	// held so it reads what is decided, not what was read before the lock.
	Allowed func(db.Approval) error
}

// decideApproval answers a request, in one transaction: the session taken
// first, as every writer of a run's events takes it, then the request, held
// until the answer is written; the answer, its event on the asking run, and
// the root run queued again when nothing of its tree waits any more. A
// request past its time is marked expired instead, and the run resumes with
// that: ErrExpired, committed. Anything else no longer pending is
// ErrNotPending, with the request as it stands.
func decideApproval(ctx context.Context, s TxStore, d decision) (db.Approval, *Resumed, error) {
	var (
		out     db.Approval
		again   *Resumed
		outcome error
	)
	err := s.InTx(ctx, func(q *db.Queries) error {
		found, err := q.GetApproval(ctx, db.GetApprovalParams{ID: d.ID, TeamID: d.TeamID})
		if err != nil {
			return err
		}
		if _, err := q.LockRunSession(ctx, found.RunID); err != nil {
			return fmt.Errorf("lock the session of run %s: %w", found.RunID, err)
		}
		a, err := q.LockApproval(ctx, db.LockApprovalParams{ID: d.ID, TeamID: d.TeamID})
		if err != nil {
			return err
		}
		out = a
		if a.Status != "pending" {
			outcome = ErrNotPending
			return nil
		}

		status, via, by, scope, reason := d.Status, d.Via, &d.By, &d.Scope, d.Reason
		if !time.Now().Before(a.ExpiresAt) {
			status, via, by, scope, reason, outcome = "expired", "sweep", nil, nil, nil, ErrExpired
		} else if err := d.Allowed(a); err != nil {
			return err
		}

		if out, err = q.DecideApproval(ctx, db.DecideApprovalParams{
			ID: a.ID, TeamID: a.TeamID, Status: status, DecidedBy: by, DecidedVia: &via,
			DecisionScope: scope, Reason: reason,
		}); err != nil {
			return fmt.Errorf("record the answer: %w", err)
		}
		payload, err := json.Marshal(decided{
			ApprovalID: out.ID, Decision: out.Status, By: out.DecidedBy, Via: out.DecidedVia, Scope: out.DecisionScope,
		})
		if err != nil {
			return fmt.Errorf("encode the answer: %w", err)
		}
		if _, err := q.AppendBrainRunEvent(ctx, db.AppendBrainRunEventParams{
			RunID: a.RunID, Kind: "approval_decided", Payload: payload,
		}); err != nil {
			return fmt.Errorf("record the answer on run %s: %w", a.RunID, err)
		}
		row, err := q.RequeueAnswered(ctx, a.RootRunID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("queue run %s again: %w", a.RootRunID, err)
		}
		again = &row
		return nil
	})
	if err != nil {
		return db.Approval{}, nil, err
	}
	return out, again, outcome
}

// decided is the approval_decided event, as the run's trace and the
// conversation's stream show it.
type decided struct {
	ApprovalID uuid.UUID  `json:"approval_id"`
	Decision   string     `json:"decision"`
	By         *uuid.UUID `json:"by"`
	Via        *string    `json:"via"`
	Scope      *string    `json:"scope,omitempty"`
}
