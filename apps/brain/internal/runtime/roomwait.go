package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A workspace call the sandbox refused before anything ran, for want of
// room, waits for room: the call is not made, the run parks, and the sweep
// makes the room itself, so a try costs no model turn. It is tried a minute
// after the refusal, then after as long as it has waited, at most
// roomRetryMax apart, and given up at the first try an hour after the first
// refusal. A try is due a few seconds early (ListDueRoomWaits), so a wait
// pushed just after one sweep is tried at the sweep its gap ends on, not the
// one after; a slow Ensure or a full batch still slips it a sweep. The three
// two-minute values below are unrelated.
const (
	roomRetryMin    = time.Minute
	roomRetryMax    = 2 * time.Minute
	roomGiveUpAfter = time.Hour
	// roomWakeBatch bounds one sweep's tries; each may wait for a room to
	// start.
	roomWakeBatch   = 10
	roomWakeTimeout = 2 * time.Minute
	// roomWakeBudget is how long a sweep starts tries for: none begins after
	// it, and a wait it skips stays due, first in line for the next sweep.
	roomWakeBudget = 2 * time.Minute
)

// How a wait ended, as room_waits.ended stores it.
const (
	roomEndedRoom       = db.RoomWaitRoom
	roomEndedGaveUp     = db.RoomWaitGaveUp
	roomEndedSuperseded = db.RoomWaitSuperseded
	roomEndedCancelled  = db.RoomWaitCancelled
)

const (
	waitingForRoom = "Not made: the team's sandbox has no room for the workspace now (%s), so nothing ran. " +
		"This run waits for room, for up to an hour, and you are told when there is, to make the call again. " +
		"Do not try another way meanwhile: say in a line that you are waiting for the sandbox, and end your turn."
	// waitingQuietly is what a conversation is told of a run that parked to
	// wait for room without saying so.
	waitingQuietly = "Waiting for room in the team's sandbox; I'll carry on once there is."

	roomCame       = "The workspace has room now: make the calls that waited for it again."
	roomGaveUp     = "The sandbox still had no room after an hour (%s), so the calls that waited for it were not made. Go on without the workspace, or tell the person it is not available now."
	roomSuperseded = "The person wrote again while the workspace waited for room, so the calls that waited were not made. Read what they wrote."
)

// RoomWaits keeps a run's wait for sandbox room; *store.Store is one. A
// workspace without it, as in a test of the tools alone, refuses as it did
// before waits.
type RoomWaits interface {
	WaitForRoom(ctx context.Context, run uuid.UUID, reason string, firstSeconds float64) (db.RoomWait, error)
}

// roomShort is a refusal in a few words, for the model.
func roomShort(err error) string {
	switch {
	case errors.Is(err, sandbox.ErrPreparing):
		return "it is still being prepared"
	case errors.Is(err, sandbox.ErrUnavailable):
		return "its provider did not answer"
	}
	return "it is full"
}

// wait keeps the run's wait for room and is what the model reads instead of
// the call's result. The first refusal opens it; every call after it in the
// attempt is refused here without asking the provider again. What the model
// and the wait are told is the refusal in a few words: the provider's own
// error can name its internal address, so it goes to the log alone. A wait
// that cannot be kept, or that the run may not open (it no longer runs, or a
// person or a cancel ended it), is the refusal, as it was before waits, in
// the same few words.
func (w *runWorkspace) wait(ctx context.Context, refused error) error {
	short := roomShort(refused)
	w.deps.Logger.InfoContext(ctx, "The sandbox had no room for a workspace call; the run waits",
		"run", w.run, "err", refused)
	if _, err := w.deps.Waits.WaitForRoom(context.WithoutCancel(ctx), w.run, short, roomRetryMin.Seconds()); err != nil {
		said := "the workspace could not be started: " + short
		if !errors.Is(err, pgx.ErrNoRows) {
			said += " (and its wait for room was not kept)"
			w.deps.Logger.WarnContext(ctx, "A wait for room was not kept", "run", w.run, "err", err)
		}
		return &refusal{said: said, err: refused}
	}
	w.waiting = toModel(fmt.Sprintf(waitingForRoom, short))
	return w.waiting
}

// refusal is a refusal in a few words that still is the provider's error to
// errors.Is: the model reads only said.
type refusal struct {
	said string
	err  error
}

func (r *refusal) Error() string { return r.said }
func (r *refusal) Unwrap() error { return r.err }

// roomWaitsEnded is what a resumed run is told of its waits that ended, and
// the waits it told.
func (e *Executor) roomWaitsEnded(ctx context.Context, runID uuid.UUID) ([]string, []uuid.UUID, error) {
	waits, err := e.store.ListUntoldRoomWaits(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading how run %s's waits for room ended: %w", runID, err)
	}
	var (
		parts []string
		ids   []uuid.UUID
	)
	for _, w := range waits {
		ids = append(ids, w.ID)
		if w.Ended == nil {
			continue // ListUntoldRoomWaits reads only ended ones
		}
		switch *w.Ended {
		case roomEndedRoom:
			parts = append(parts, roomCame)
		case roomEndedGaveUp:
			said, _ := clip(oneLine(w.Reason)) // whether it was clipped says nothing to the model
			parts = append(parts, fmt.Sprintf(roomGaveUp, said))
		case roomEndedSuperseded:
			parts = append(parts, roomSuperseded)
		default:
			// A cancelled wait belongs to a run that ended, which is not
			// resumed to be told; anything else is a wait this brain does not
			// know, told as the room coming, which costs a call at most.
			parts = append(parts, roomCame)
		}
	}
	return parts, ids, nil
}

// wakeRoomWaits tries each wait whose time has come: the room made, as the
// run's call would make it, and the run's root queued again once nothing
// else of its tree waits; still no room, the next try later; an hour gone,
// given up, and the root queued so its model is told. The room is made
// without the session held, as a call makes it; the wait is ended and the
// root queued under it, as an answer is. Tries start for roomWakeBudget at
// most, so a slow provider holds up one sweep, not the minute's next ones.
func (j *Job) wakeRoomWaits(ctx context.Context) []error {
	due, err := j.store.ListDueRoomWaits(ctx, db.ListDueRoomWaitsParams{
		GiveUpSeconds: roomGiveUpAfter.Seconds(), Batch: roomWakeBatch,
	})
	if err != nil {
		return []error{fmt.Errorf("listing waits for room due a try: %w", err)}
	}
	stop := time.Now().Add(j.wakeBudget)
	var failed []error
	for _, w := range due {
		if !w.GaveUp && !time.Now().Before(stop) {
			continue // a wait given up needs no try, so the rest are still read
		}
		again, err := j.tryWait(ctx, w)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if again == nil {
			continue
		}
		if err := j.resume(again.ID, again.Attempt); err != nil {
			failed = append(failed, err)
		}
	}
	return failed
}

// tryWait is one due wait: given up, ended with the room made (or refused in
// a way waiting cannot end), or put off; the root to queue, when ending it
// left nothing of its tree waiting.
func (j *Job) tryWait(ctx context.Context, w db.ListDueRoomWaitsRow) (*Resumed, error) {
	if w.GaveUp {
		return endRoomWait(ctx, j.store, w, roomEndedGaveUp)
	}
	waitable, err := j.makeRoom(ctx, w)
	if err != nil {
		return nil, err
	}
	if !waitable {
		return endRoomWait(ctx, j.store, w, roomEndedRoom)
	}
	if _, err := j.store.PushRoomWait(ctx, db.PushRoomWaitParams{
		ID: w.ID, MinSeconds: roomRetryMin.Seconds(), MaxSeconds: roomRetryMax.Seconds(),
	}); err != nil {
		return nil, fmt.Errorf("putting off wait %s: %w", w.ID, err)
	}
	return nil, nil
}

// makeRoom makes the wait's room: waitable when the sandbox still has no
// room for it. Any other answer ends the wait, a room made or not: the run's
// call meets whatever it is, and its model reads that, as it did before
// waits. Only a worker stopping is an error.
func (j *Job) makeRoom(ctx context.Context, w db.ListDueRoomWaitsRow) (bool, error) {
	ws := j.executor.builder.workspaces
	if ws == nil {
		return false, nil
	}
	tctx, cancel := context.WithTimeout(ctx, roomWakeTimeout)
	defer cancel()
	_, err := ws.Rooms.Ensure(tctx, w.TeamID, w.SessionID)
	switch {
	case ctx.Err() != nil:
		return false, fmt.Errorf("making the room wait %s waits for: %w", w.ID, ctx.Err())
	case sandbox.Waitable(err):
		return true, nil
	case err != nil:
		j.logger.InfoContext(ctx, "A wait for room ended on another refusal", "wait", w.ID, "err", err)
	}
	return false, nil
}

// endRoomWait ends one wait, the session of its root held first as every
// writer of a run takes it, and queues the root again when nothing else of
// its tree waits.
func endRoomWait(ctx context.Context, s TxStore, w db.ListDueRoomWaitsRow, ended string) (*Resumed, error) {
	var again *Resumed
	err := s.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockRunSession(ctx, w.RootRunID); err != nil {
			return fmt.Errorf("lock the session of run %s: %w", w.RootRunID, err)
		}
		n, err := q.EndRoomWait(ctx, db.EndRoomWaitParams{ID: w.ID, Ended: ended})
		if err != nil || n == 0 {
			return err
		}
		row, err := q.RequeueAnswered(ctx, w.RootRunID)
		if err == nil {
			again = &row
			return nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// Still running or parking, or waiting on more: its park, or the
			// last of what it waits for, queues it.
			return nil
		}
		return fmt.Errorf("queue run %s again: %w", w.RootRunID, err)
	})
	if err != nil {
		return nil, fmt.Errorf("ending wait %s: %w", w.ID, err)
	}
	return again, nil
}
