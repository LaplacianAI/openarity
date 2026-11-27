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
// after the refusal, then after as long as it has waited, at most ten minutes
// apart, and given up an hour after the first refusal.
const (
	roomRetryMin    = time.Minute
	roomRetryMax    = 10 * time.Minute
	roomGiveUpAfter = time.Hour
	// roomWakeBatch bounds one sweep's tries; each may wait for a room to
	// start.
	roomWakeBatch   = 10
	roomWakeTimeout = 2 * time.Minute
)

// How a wait ended, as room_waits.ended stores it.
const (
	roomEndedRoom       = "room"
	roomEndedGaveUp     = "gave_up"
	roomEndedSuperseded = "superseded"
	roomEndedCancelled  = "cancelled"
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

// RoomWaits keeps a run's wait for sandbox room.
type RoomWaits interface {
	OpenRoomWait(ctx context.Context, arg db.OpenRoomWaitParams) (db.RoomWait, error)
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
// attempt is refused here without asking the provider again. A wait that
// cannot be kept is the refusal, as it was before waits.
func (w *runWorkspace) wait(ctx context.Context, refused error) error {
	if _, err := w.deps.Waits.OpenRoomWait(context.WithoutCancel(ctx), db.OpenRoomWaitParams{
		TeamID: w.team, RunID: w.run, RootRunID: w.root, SessionID: w.session,
		Reason: refused.Error(), FirstSeconds: roomRetryMin.Seconds(),
	}); err != nil {
		return fmt.Errorf("the workspace could not be started: %w (and its wait for room was not kept: %w)", refused, err)
	}
	w.waiting = toModel(fmt.Sprintf(waitingForRoom, roomShort(refused)))
	return w.waiting
}

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
		switch *w.Ended {
		case roomEndedRoom:
			parts = append(parts, roomCame)
		case roomEndedGaveUp:
			said, _ := clip(oneLine(w.Reason))
			parts = append(parts, fmt.Sprintf(roomGaveUp, said))
		case roomEndedSuperseded:
			parts = append(parts, roomSuperseded)
		}
	}
	return parts, ids, nil
}

// wakeRoomWaits tries each wait whose time has come: the room made, as the
// run's call would make it, and the run's root queued again once nothing
// else of its tree waits; still no room, the next try later; an hour gone,
// given up, and the root queued so its model is told. The room is made
// without the session held, as a call makes it; the wait is ended and the
// root queued under it, as an answer is.
func (j *Job) wakeRoomWaits(ctx context.Context) []error {
	due, err := j.store.ListDueRoomWaits(ctx, db.ListDueRoomWaitsParams{
		GiveUpSeconds: roomGiveUpAfter.Seconds(), Batch: roomWakeBatch,
	})
	if err != nil {
		return []error{fmt.Errorf("listing waits for room due a try: %w", err)}
	}
	var failed []error
	for _, w := range due {
		ended := roomEndedGaveUp
		if !w.GaveUp {
			waitable, err := j.makeRoom(ctx, w)
			if err != nil {
				failed = append(failed, err)
				continue
			}
			if waitable {
				if _, err := j.store.PushRoomWait(ctx, db.PushRoomWaitParams{
					ID: w.ID, MinSeconds: roomRetryMin.Seconds(), MaxSeconds: roomRetryMax.Seconds(),
				}); err != nil {
					failed = append(failed, fmt.Errorf("putting off wait %s: %w", w.ID, err))
				}
				continue
			}
			ended = roomEndedRoom
		}
		again, err := endRoomWait(ctx, j.store, w, ended)
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
