package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// An agent's change to what a schedule or watcher does waits for its maker's
// yes, as a proposal does (D10): text a run read cannot rewrite what runs as
// a person. One still proposed is revised in place, and the revising run may
// not confirm it. One that runs keeps running as it was, with the change held
// beside it until a later message from its maker confirms it, or the hour
// passes and the sweep drops it. A person's own change replaces one waiting.

// ProposeScheduleChange holds an agent's change to one of the caller's own
// schedules for them to confirm.
func (s *Service) ProposeScheduleChange(ctx context.Context, who Who, origin Origin, ref Ref, p SchedulePatch) (ScheduleView, error) {
	if !origin.viaTool() {
		return ScheduleView{}, refuse(Invalid, "only an agent's change waits to be confirmed")
	}
	if p.Status != nil || !p.changesWhatItDoes() {
		return ScheduleView{}, refuse(Invalid, "say what to change: its name, objective, time, zone or agent")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return ScheduleView{}, err
	}
	now := s.now()
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if err := ownsIt(who, *row); err != nil {
			return err
		}
		switch row.Status {
		case Proposed:
			row.ProposedRun, row.ProposedIn = &origin.Run, &origin.Session
			return applyPatch(ctx, q, row, p, l, now)
		case Active, Paused:
		default:
			return refuse(Conflict, "this schedule is %s, so it cannot be changed", row.Status)
		}
		// Checked now as it will be applied, on a copy: the schedule itself
		// runs on as it was until the change is confirmed.
		tried := *row
		if err := applyPatch(ctx, q, &tried, p, l, now); err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("keep the change: %w", err)
		}
		row.Pending, row.PendingRun, row.PendingIn, row.PendingAt = raw, &origin.Run, &origin.Session, &now
		return nil
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// confirmScheduleChange applies the change waiting on row, checked again as
// it is now.
func confirmScheduleChange(ctx context.Context, q *db.Queries, who Who, origin Origin, row *db.Schedule, l Limits, now time.Time) error {
	if err := changeConfirmable(ctx, q, who, origin, pendingOf(row.PendingRun, row.PendingIn, row.PendingAt, row.CreatedBy, "schedule"), now); err != nil {
		return err
	}
	var p SchedulePatch
	if err := json.Unmarshal(row.Pending, &p); err != nil {
		return fmt.Errorf("read the waiting change: %w", err)
	}
	if err := applyPatch(ctx, q, row, p, l, now); err != nil {
		return err
	}
	dropPending(&row.Pending, &row.PendingRun, &row.PendingIn, &row.PendingAt)
	return nil
}

// ProposeWatcherChange holds an agent's change to one of the caller's own
// watchers for them to confirm.
func (s *Service) ProposeWatcherChange(ctx context.Context, who Who, origin Origin, ref Ref, p WatcherPatch) (WatcherView, error) {
	if !origin.viaTool() {
		return WatcherView{}, refuse(Invalid, "only an agent's change waits to be confirmed")
	}
	if p.Status != nil || !p.changesWhatItDoes() {
		return WatcherView{}, refuse(Invalid, "say what to change: its name, objective, how often, cooldown, selector or agent")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return WatcherView{}, err
	}
	now := s.now()
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		if !madeBy(who, row.CreatedBy) {
			return refuse(Forbidden, "only the person who made this watcher may change what it does or check it now")
		}
		switch row.Status {
		case Proposed, WaitingSecret:
			row.ProposedRun, row.ProposedIn = &origin.Run, &origin.Session
			return patchWatcher(ctx, q, row, p, l)
		case Active, Paused:
		default:
			return refuse(Conflict, "this watcher is %s, so it cannot be changed", row.Status)
		}
		tried := *row
		if err := patchWatcher(ctx, q, &tried, p, l); err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("keep the change: %w", err)
		}
		row.Pending, row.PendingRun, row.PendingIn, row.PendingAt = raw, &origin.Run, &origin.Session, &now
		return nil
	})
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

func confirmWatcherChange(ctx context.Context, q *db.Queries, who Who, origin Origin, row *db.Watcher, l Limits, now time.Time) error {
	if err := changeConfirmable(ctx, q, who, origin, pendingOf(row.PendingRun, row.PendingIn, row.PendingAt, row.CreatedBy, "watcher"), now); err != nil {
		return err
	}
	var p WatcherPatch
	if err := json.Unmarshal(row.Pending, &p); err != nil {
		return fmt.Errorf("read the waiting change: %w", err)
	}
	if err := patchWatcher(ctx, q, row, p, l); err != nil {
		return err
	}
	dropPending(&row.Pending, &row.PendingRun, &row.PendingIn, &row.PendingAt)
	return nil
}

// pendingOf is a waiting change as confirming it reads it.
func pendingOf(run, in *uuid.UUID, at *time.Time, maker *uuid.UUID, noun string) proposal {
	p := proposal{run: run, in: in, maker: maker, noun: "change to a " + noun}
	if at != nil {
		p.at = *at
	}
	return p
}

// changeConfirmable is whether who may confirm the change p now: its maker,
// within the hour, and from a tool only by a later message of theirs in the
// conversation the change was proposed in.
func changeConfirmable(ctx context.Context, q *db.Queries, who Who, origin Origin, p proposal, now time.Time) error {
	if p.maker == nil || *p.maker != who.User {
		return refuse(Forbidden, "only the person who made it may confirm a change to it")
	}
	if now.Sub(p.at) > proposalLife {
		return refuse(Conflict, "the change waited more than an hour and lapsed; ask for it again")
	}
	if origin.viaTool() {
		return laterMessage(ctx, q, p, origin)
	}
	return nil
}

func dropPending(pending *[]byte, run, in **uuid.UUID, at **time.Time) {
	*pending, *run, *in, *at = nil, nil, nil, nil
}

// WaitingChange is what an agent's change waiting on the schedule sets, said
// for a person, and when it was proposed; empty when none waits.
func (v ScheduleView) WaitingChange() (string, *time.Time) {
	var p SchedulePatch
	if v.Pending == nil || json.Unmarshal(v.Pending, &p) != nil {
		return "", nil
	}
	return patchSays(p), v.PendingAt
}

// WaitingChange is what an agent's change waiting on the watcher sets, and
// when it was proposed; empty when none waits.
func (v WatcherView) WaitingChange() (string, *time.Time) {
	var p WatcherPatch
	if v.Pending == nil || json.Unmarshal(v.Pending, &p) != nil {
		return "", nil
	}
	return watchPatchSays(p), v.PendingAt
}
