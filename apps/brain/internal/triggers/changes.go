package triggers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"

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
//
// Each proposal, revision and ask has a code of its own, which the tool's
// answer gives for the person to send back: a message is a yes only if it
// carries the code. A run that read a page saying to confirm cannot make the
// person type it, where a "thanks" would otherwise start a run that confirms
// first (*user*, 2026-11-28).

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

// yesCodeDigits is how long a yes code is: short to type, and unlikely to
// be in a message by chance.
const yesCodeDigits = 4

// newYesCode is a code for a person to confirm with, random so that nothing
// but the tool's answer to the model says it.
func newYesCode() *string {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	code := fmt.Sprintf("%0*d", yesCodeDigits, n.Int64())
	return &code
}

// carriesCode is whether text holds code as a number of its own: "yes 4821"
// or "4821, go ahead", not "14821".
func carriesCode(text, code string) bool {
	for _, n := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsDigit(r) }) {
		if n == code {
			return true
		}
	}
	return false
}

// waiting is a row's columns for an ask that waits on it, and who made the
// row, who alone confirms it.
type waiting struct {
	pending *[]byte
	run, in **uuid.UUID
	at      **time.Time
	code    **string
	maker   *uuid.UUID
}

func waitingOnSchedule(r *db.Schedule) waiting {
	return waiting{pending: &r.Pending, run: &r.PendingRun, in: &r.PendingIn, at: &r.PendingAt, code: &r.YesCode, maker: r.CreatedBy}
}

func waitingOnWatcher(r *db.Watcher) waiting {
	return waiting{pending: &r.Pending, run: &r.PendingRun, in: &r.PendingIn, at: &r.PendingAt, code: &r.YesCode, maker: r.CreatedBy}
}

// hold keeps a as the ask waiting, with a new code to confirm it.
func (w waiting) hold(a ask, origin Origin, now time.Time) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("keep the change: %w", err)
	}
	run, in := origin.Run, origin.Session
	*w.pending, *w.run, *w.in, *w.at, *w.code = raw, &run, &in, &now, newYesCode()
	return nil
}

// drop lets go of the ask waiting, and its code; a proposal's code stays.
func (w waiting) drop() {
	if *w.pending != nil {
		*w.code = nil
	}
	*w.pending, *w.run, *w.in, *w.at = nil, nil, nil, nil
}

// proposal is the waiting ask as confirming it reads it.
func (w waiting) proposal() proposal {
	p := proposal{run: *w.run, in: *w.in, maker: w.maker, code: *w.code, noun: "change"}
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
			row.ProposedRun, row.ProposedIn, row.ProposedAt, row.YesCode = &origin.Run, &origin.Session, &now, newYesCode()
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
		deleteNow, err := actionRule(action, row.Status, row.Status == Proposed, "schedule")
		if err != nil {
			return nil, err
		}
		if deleteNow {
			return nil, deleteRow(ctx, q, row, s.now())
		}
		return &ask{Action: action}, nil
	})
}

// actionRule is what an agent's delete, resume or run now of one may do, for
// schedules and watchers alike: one not yet confirmed is deleted at once, and
// is asked nothing else; one that runs is asked, and only a paused one to
// resume.
func actionRule(action, status string, unconfirmed bool, noun string) (deleteNow bool, err error) {
	switch {
	case action != askDelete && action != askResume && action != askRunNow:
		return false, refuse(Invalid, "an agent asks to delete, resume or run one now")
	case action == askDelete && unconfirmed:
		return true, nil
	case unconfirmed:
		return false, refuse(Conflict, "confirm this %s first", noun)
	case action == askResume && status != Paused:
		return false, refuse(Conflict, "only a paused %s can be resumed; this one is %s", noun, status)
	}
	return false, nil
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
			// One waiting for its secret is confirmed by making it, on the
			// dashboard, so it has no code.
			row.ProposedRun, row.ProposedIn, row.ProposedAt = &origin.Run, &origin.Session, &now
			if row.Status == Proposed {
				row.YesCode = newYesCode()
			}
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
// is deleted at once, in the same transaction, and its secret after.
func (s *Service) ProposeWatcherAction(ctx context.Context, who Who, origin Origin, ref Ref, action string) (WatcherView, error) {
	var hooked *db.Watcher
	v, err := s.askOfWatcher(ctx, who, origin, ref, func(q *db.Queries, row *db.Watcher) (*ask, error) {
		deleteNow, err := actionRule(action, row.Status, row.Status == Proposed || row.Status == WaitingSecret, "watcher")
		switch {
		case err != nil:
			return nil, err
		case deleteNow:
			if hooked, err = deleteWatcherRow(ctx, q, row, s.now()); err != nil {
				return nil, err
			}
			return nil, nil
		case action == askRunNow && row.Source == SourceWebhook:
			return nil, refuse(Invalid, "a webhook watcher runs when its URL is called; there is nothing for it to look at")
		}
		return &ask{Action: action}, nil
	})
	if err != nil {
		return WatcherView{}, err
	}
	s.destroySecret(ctx, hooked)
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
// message of theirs in the conversation it was asked in, carrying its code.
// It is taken before it is acted on, so two yeses act once.
func takeAsk(ctx context.Context, q *db.Queries, who Who, origin Origin, w waiting, now time.Time) (ask, error) {
	p := w.proposal()
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
	a, err := takeAsk(ctx, q, who, origin, waitingOnSchedule(row), now)
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
	return s.confirmedButNot(ctx, id, err, action)
}

func confirmWatcherAsk(ctx context.Context, q *db.Queries, who Who, origin Origin, row *db.Watcher, l Limits, now time.Time) (string, error) {
	a, err := takeAsk(ctx, q, who, origin, waitingOnWatcher(row), now)
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
	return s.confirmedButNot(ctx, id, err, action)
}

// confirmedButNot is an ask confirmed and taken whose act failed: it is not
// tried again, so it is asked for again. A refusal says why; a failure is
// kept for whoever runs the brain. A crash between the take and the act ends
// the same way, with nothing done.
func (s *Service) confirmedButNot(ctx context.Context, id uuid.UUID, err error, action string) error {
	if err == nil {
		return nil
	}
	what := strings.ReplaceAll(action, "_", " ")
	if k := KindOf(err); k != 0 {
		return refuse(k, "confirmed, but it could not %s: %s; ask for it again", what, err.Error())
	}
	s.logger.ErrorContext(ctx, "A confirmed ask was taken and could not be done", "id", id, "action", action, "error", err)
	return refuse(Conflict, "confirmed, but it could not %s just now; ask for it again", what)
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
		// Never so for an ask this brain kept; one it no longer reads still
		// waits, so it is said to.
		return "for something it can no longer read; ask for it again", at
	}
	return a.says(), at
}
