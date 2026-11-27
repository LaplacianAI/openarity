package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// What an agent asks of a schedule or watcher that runs waits for its
// maker's yes, as a proposal does (D10): text a run read cannot rewrite, run,
// resume or delete what runs as a person (*user*, 2026-11-27). It runs on as
// it was, with the ask held beside it until a later message from its maker
// confirms it, or the hour passes and the sweep drops it. Only a pause acts
// at once, as it only stops runs. A person's own change replaces one
// waiting, as does the agent's next ask. One still proposed is revised in
// place, and the revising run may not confirm it.

// What an agent may ask of one that runs.
const (
	askChange = "change"
	askDelete = "delete"
	askResume = "resume"
	askRunNow = "run_now"
)

// An ask is what an agent asked of one that runs, as it waits in pending.
type ask struct {
	Action   string         `json:"action"`
	Schedule *SchedulePatch `json:"schedule,omitempty"`
	Watcher  *WatcherPatch  `json:"watcher,omitempty"`
}

// says is the ask said for a person: what it changes, or what it does.
func (a ask) says() string {
	switch a.Action {
	case askDelete:
		return "to delete it"
	case askResume:
		return "to resume it"
	case askRunNow:
		return "to run it now"
	}
	switch {
	case a.Schedule != nil:
		return "to change " + patchSays(*a.Schedule)
	case a.Watcher != nil:
		return "to change " + watcherPatchSays(*a.Watcher)
	}
	return ""
}

func askOf(raw []byte) (ask, error) {
	var a ask
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("read the waiting change: %w", err)
	}
	return a, nil
}

// waiting is a row's columns for an ask that waits on it.
type waiting struct {
	pending *[]byte
	run, in **uuid.UUID
	at      **time.Time
}

func waitingOnSchedule(r *db.Schedule) waiting {
	return waiting{pending: &r.Pending, run: &r.PendingRun, in: &r.PendingIn, at: &r.PendingAt}
}

func waitingOnWatcher(r *db.Watcher) waiting {
	return waiting{pending: &r.Pending, run: &r.PendingRun, in: &r.PendingIn, at: &r.PendingAt}
}

func (w waiting) hold(a ask, origin Origin, now time.Time) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("keep the change: %w", err)
	}
	run, in := origin.Run, origin.Session
	*w.pending, *w.run, *w.in, *w.at = raw, &run, &in, &now
	return nil
}

func (w waiting) drop() { *w.pending, *w.run, *w.in, *w.at = nil, nil, nil, nil }

// proposal is the waiting ask as confirming it reads it, made by maker.
func (w waiting) proposal(maker *uuid.UUID) proposal {
	p := proposal{run: *w.run, in: *w.in, maker: maker, noun: "change"}
	if *w.at != nil {
		p.at = **w.at
	}
	return p
}

// ProposeScheduleChange holds an agent's change to what one of the caller's
// own schedules does for them to confirm, or revises one still proposed.
func (s *Service) ProposeScheduleChange(ctx context.Context, who Who, origin Origin, ref Ref, p SchedulePatch) (ScheduleView, error) {
	if p.Status != nil || !p.changesWhatItDoes() {
		return ScheduleView{}, refuse(Invalid, "say what to change: its name, objective, time, zone or agent")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return ScheduleView{}, err
	}
	now := s.now()
	return s.askOfSchedule(ctx, who, origin, ref, func(q *db.Queries, row *db.Schedule) (*ask, error) {
		if row.Status == Proposed {
			row.ProposedRun, row.ProposedIn, row.ProposedAt = &origin.Run, &origin.Session, &now
			return nil, applyPatch(ctx, q, row, p, l, now)
		}
		// Checked now as it will be applied, on a copy: the schedule itself
		// runs on as it was until the change is confirmed.
		tried := *row
		if err := applyPatch(ctx, q, &tried, p, l, now); err != nil {
			return nil, err
		}
		return &ask{Action: askChange, Schedule: &p}, nil
	})
}

// ProposeScheduleAction holds an agent's delete, resume or run now of one of
// the caller's own schedules for them to confirm. A proposal never ran, so
// it is deleted at once.
func (s *Service) ProposeScheduleAction(ctx context.Context, who Who, origin Origin, ref Ref, action string) (ScheduleView, error) {
	return s.askOfSchedule(ctx, who, origin, ref, func(q *db.Queries, row *db.Schedule) (*ask, error) {
		switch {
		case action == askDelete && row.Status == Proposed:
			return nil, deleteRow(ctx, q, row, s.now())
		case row.Status == Proposed:
			return nil, refuse(Conflict, "confirm this schedule first")
		case action == askResume && row.Status != Paused:
			return nil, refuse(Conflict, "only a paused schedule can be resumed; this one is %s", row.Status)
		case action != askDelete && action != askResume && action != askRunNow:
			return nil, refuse(Invalid, "an agent asks to delete, resume or run one now")
		}
		return &ask{Action: action}, nil
	})
}

// askOfSchedule holds what decide asks of one of the caller's own schedules,
// or applies what it changed itself when it asks nothing.
func (s *Service) askOfSchedule(ctx context.Context, who Who, origin Origin, ref Ref,
	decide func(*db.Queries, *db.Schedule) (*ask, error),
) (ScheduleView, error) {
	if !origin.viaTool() {
		return ScheduleView{}, refuse(Invalid, "only an agent's ask waits to be confirmed")
	}
	now := s.now()
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if err := ownsIt(who, *row); err != nil {
			return err
		}
		if row.Status != Proposed && row.Status != Active && row.Status != Paused {
			return refuse(Conflict, "this schedule is %s, so it cannot be changed", row.Status)
		}
		a, err := decide(q, row)
		if err != nil || a == nil {
			return err
		}
		return waitingOnSchedule(row).hold(*a, origin, now)
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// ProposeWatcherChange holds an agent's change to what one of the caller's
// own watchers does for them to confirm, or revises one still proposed.
func (s *Service) ProposeWatcherChange(ctx context.Context, who Who, origin Origin, ref Ref, p WatcherPatch) (WatcherView, error) {
	if p.Status != nil || !p.changesWhatItDoes() {
		return WatcherView{}, refuse(Invalid, "say what to change: its name, objective, how often, cooldown, selector or agent")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return WatcherView{}, err
	}
	now := s.now()
	return s.askOfWatcher(ctx, who, origin, ref, func(q *db.Queries, row *db.Watcher) (*ask, error) {
		if row.Status == Proposed || row.Status == WaitingSecret {
			row.ProposedRun, row.ProposedIn, row.ProposedAt = &origin.Run, &origin.Session, &now
			return nil, patchWatcher(ctx, q, row, p, l)
		}
		tried := *row
		if err := patchWatcher(ctx, q, &tried, p, l); err != nil {
			return nil, err
		}
		return &ask{Action: askChange, Watcher: &p}, nil
	})
}

// ProposeWatcherAction holds an agent's delete, resume or look now of one of
// the caller's own watchers for them to confirm. A proposal never ran, so it
// is deleted at once.
func (s *Service) ProposeWatcherAction(ctx context.Context, who Who, origin Origin, ref Ref, action string) (WatcherView, error) {
	proposed := false
	v, err := s.askOfWatcher(ctx, who, origin, ref, func(_ *db.Queries, row *db.Watcher) (*ask, error) {
		unconfirmed := row.Status == Proposed || row.Status == WaitingSecret
		switch {
		case action == askDelete && unconfirmed:
			proposed = true
			return nil, nil
		case unconfirmed:
			return nil, refuse(Conflict, "confirm this watcher first")
		case action == askResume && row.Status != Paused:
			return nil, refuse(Conflict, "only a paused watcher can be resumed; this one is %s", row.Status)
		case action == askRunNow && row.Source == SourceWebhook:
			return nil, refuse(Invalid, "a webhook watcher runs when its URL is called; there is nothing for it to look at")
		case action != askDelete && action != askResume && action != askRunNow:
			return nil, refuse(Invalid, "an agent asks to delete, resume or check one now")
		}
		return &ask{Action: action}, nil
	})
	if err != nil || !proposed {
		return v, err
	}
	// Its secret, if one was made, goes with it, as any delete's does.
	if err := s.DeleteWatcher(ctx, who, Ref{ID: v.ID}); err != nil {
		return WatcherView{}, err
	}
	v.Status = Deleted
	return v, nil
}

func (s *Service) askOfWatcher(ctx context.Context, who Who, origin Origin, ref Ref,
	decide func(*db.Queries, *db.Watcher) (*ask, error),
) (WatcherView, error) {
	if !origin.viaTool() {
		return WatcherView{}, refuse(Invalid, "only an agent's ask waits to be confirmed")
	}
	now := s.now()
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		if !madeBy(who, row.CreatedBy) {
			return refuse(Forbidden, "only the person who made this watcher may change what it does or check it now")
		}
		switch row.Status {
		case Proposed, WaitingSecret, Active, Paused:
		default:
			return refuse(Conflict, "this watcher is %s, so it cannot be changed", row.Status)
		}
		a, err := decide(q, row)
		if err != nil || a == nil {
			return err
		}
		return waitingOnWatcher(row).hold(*a, origin, now)
	})
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

// takeAsk checks who may confirm the ask waiting on a row now, and takes it
// off the row: its maker, within the hour, and from a tool only by a later
// message of theirs in the conversation it was asked in. It is taken before
// it is acted on, so two yeses act once.
func takeAsk(ctx context.Context, q *db.Queries, who Who, origin Origin, w waiting, maker *uuid.UUID, now time.Time) (ask, error) {
	p := w.proposal(maker)
	if p.maker == nil || *p.maker != who.User {
		return ask{}, refuse(Forbidden, "only the person who made it may confirm a change to it")
	}
	if now.Sub(p.at) > proposalLife {
		return ask{}, refuse(Conflict, "the change waited more than an hour and lapsed; ask for it again")
	}
	if origin.viaTool() {
		if err := laterMessage(ctx, q, p, origin); err != nil {
			return ask{}, err
		}
	}
	a, err := askOf(*w.pending)
	if err != nil {
		return ask{}, err
	}
	w.drop()
	return a, nil
}

// confirmScheduleAsk applies the ask waiting on row: a change in the same
// transaction, checked again as it is now; an action it leaves to act after.
func confirmScheduleAsk(ctx context.Context, q *db.Queries, who Who, origin Origin, row *db.Schedule, l Limits, now time.Time) (string, error) {
	a, err := takeAsk(ctx, q, who, origin, waitingOnSchedule(row), row.CreatedBy, now)
	if err != nil {
		return "", err
	}
	if a.Action != askChange {
		return a.Action, nil
	}
	if a.Schedule == nil {
		return "", fmt.Errorf("the waiting change on schedule %s names no change", row.ID)
	}
	return "", applyPatch(ctx, q, row, *a.Schedule, l, now)
}

// actOnSchedule does what a confirmed ask said to.
func (s *Service) actOnSchedule(ctx context.Context, who Who, id uuid.UUID, action string) error {
	var err error
	switch action {
	case askDelete:
		err = s.Delete(ctx, who, Ref{ID: id})
	case askResume:
		_, err = s.Resume(ctx, who, Ref{ID: id})
	case askRunNow:
		_, err = s.RunNow(ctx, who, Ref{ID: id})
	}
	return confirmedButNot(err, action)
}

func confirmWatcherAsk(ctx context.Context, q *db.Queries, who Who, origin Origin, row *db.Watcher, l Limits, now time.Time) (string, error) {
	a, err := takeAsk(ctx, q, who, origin, waitingOnWatcher(row), row.CreatedBy, now)
	if err != nil {
		return "", err
	}
	if a.Action != askChange {
		return a.Action, nil
	}
	if a.Watcher == nil {
		return "", fmt.Errorf("the waiting change on watcher %s names no change", row.ID)
	}
	return "", patchWatcher(ctx, q, row, *a.Watcher, l)
}

func (s *Service) actOnWatcher(ctx context.Context, who Who, id uuid.UUID, action string) error {
	var err error
	switch action {
	case askDelete:
		err = s.DeleteWatcher(ctx, who, Ref{ID: id})
	case askResume:
		active := Active
		_, err = s.UpdateWatcher(ctx, who, Ref{ID: id}, WatcherPatch{Status: &active})
	case askRunNow:
		_, err = s.CheckNow(ctx, who, Ref{ID: id})
	}
	return confirmedButNot(err, action)
}

// confirmedButNot is an ask confirmed and taken whose act was refused: it is
// not tried again, so it is asked for again.
func confirmedButNot(err error, action string) error {
	if KindOf(err) == 0 {
		return err
	}
	return refuse(KindOf(err), "confirmed, but it could not %s: %s; ask for it again",
		strings.ReplaceAll(action, "_", " "), err.Error())
}

// WaitingChange is what an agent asked of the schedule, said for a person,
// and when; empty when nothing waits.
func (v ScheduleView) WaitingChange() (string, *time.Time) {
	return waitingSays(v.Pending, v.PendingAt)
}

// WaitingChange is what an agent asked of the watcher, said for a person,
// and when; empty when nothing waits.
func (v WatcherView) WaitingChange() (string, *time.Time) {
	return waitingSays(v.Pending, v.PendingAt)
}

func waitingSays(raw []byte, at *time.Time) (string, *time.Time) {
	if raw == nil {
		return "", nil
	}
	a, err := askOf(raw)
	if err != nil {
		return "", nil
	}
	return a.says(), at
}
