package triggers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// The outcomes a fire is recorded with, beside when's.
const (
	skippedBusy   = "skipped_busy"
	skippedCap    = "skipped_cap"
	skippedMaker  = "skipped_maker"
	skippedTarget = "skipped_target"
)

const (
	// retryAfter is how long a fire that could not be tried waits to be
	// tried again, for the same time it was due.
	retryAfter = time.Minute
	// stuckAfter is how long the database's passing trouble may go on for one
	// schedule before its retries count: trouble that never passes is the
	// schedule's to show, not a silent retry each minute.
	stuckAfter = time.Hour
)

// FireStore is what firing reads and writes, in one transaction a schedule.
type FireStore interface {
	InTx(ctx context.Context, fn func(*db.Queries) error) error
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	CountDueSchedules(ctx context.Context, now time.Time) (int32, error)
	DeferFailedFire(ctx context.Context, arg db.DeferFailedFireParams) (string, error)
}

// A Firer fires the schedules that are due, one transaction each.
type Firer struct {
	store  FireStore
	runs   Enqueuer
	logger *slog.Logger
	now    func() time.Time
}

// NewFirer fires a store's due schedules and hands their runs to runs.
func NewFirer(s FireStore, runs Enqueuer, logger *slog.Logger) *Firer {
	return &Firer{store: s, runs: runs, logger: logger, now: time.Now}
}

// Due is how many schedules are due now.
func (f *Firer) Due(ctx context.Context) (int, error) {
	n, err := f.store.CountDueSchedules(ctx, f.now())
	if err != nil {
		return 0, fmt.Errorf("count the due schedules: %w", err)
	}
	return int(n), nil
}

// FireDue fires due schedules until none is due, max have been taken, or
// budget has passed, and hands each run it started to the workers as soon as
// its transaction commits. Another worker doing the same takes other rows. A
// schedule whose fire fails is put off and counted as failing, so it neither
// holds up the rest nor is taken again at once; only a failure to put it off
// ends the batch.
func (f *Firer) FireDue(ctx context.Context, maxFires int, budget time.Duration) (int, error) {
	deadline := f.now().Add(budget)
	fired := 0
	for fired < maxFires && f.now().Before(deadline) {
		claimed, run, err := f.fireOne(ctx)
		var bad *fireFailed
		if errors.As(err, &bad) && ctx.Err() == nil {
			if err := f.putOff(ctx, bad); err != nil {
				return fired, err
			}
			fired++
			continue
		}
		if err != nil {
			return fired, err
		}
		if !claimed {
			break
		}
		fired++
		if run == uuid.Nil {
			continue
		}
		if err := f.runs.Enqueue(ctx, run); err != nil {
			f.logger.WarnContext(ctx, "A fired run was not enqueued; the runtime's sweep will start it", "run_id", run, "error", err)
		}
	}
	return fired, nil
}

// fireFailed is a claimed schedule whose fire failed: its transaction rolled
// back, so it is still the oldest due.
type fireFailed struct {
	id  uuid.UUID
	err error
}

func (e *fireFailed) Error() string { return fmt.Sprintf("fire schedule %s: %v", e.id, e.err) }
func (e *fireFailed) Unwrap() error { return e.err }

// putOff has a schedule whose fire failed wait retryAfter, in a transaction
// of its own, then try the same time again. A failure of its own counts, and
// the fifth in a row pauses it; the database's passing trouble counts only
// once it has gone on for stuckAfter.
func (f *Firer) putOff(ctx context.Context, bad *fireFailed) error {
	counts := !passing(bad.err)
	now := f.now()
	f.logger.ErrorContext(ctx, "A schedule could not fire; trying it again later",
		"schedule_id", bad.id, "its_own", counts, "error", bad.err)
	_, err := f.store.DeferFailedFire(ctx, db.DeferFailedFireParams{
		ID: bad.id, Counts: counts, StuckBefore: now.Add(-stuckAfter),
		PauseAfter: store.PauseAfter, RetryAt: now.Add(retryAfter),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("put off schedule %s: %w", bad.id, err)
	}
	return nil
}

// passing is an error from the database's state rather than the schedule's:
// a lost connection (08), the server short of resources (53), a deadlock or
// a serialization failure (40), a statement timeout or the server shutting
// down (57), a lock timeout, or a connection that failed or timed out before
// any of those. Trying again is its remedy.
func passing(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		for _, class := range []string{"08", "40", "53", "57"} {
			if strings.HasPrefix(pg.Code, class) {
				return true
			}
		}
		return pg.Code == "55P03"
	}
	var nerr net.Error
	return errors.As(err, &nerr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err)
}

// fireOne takes one due schedule, works out what is due (D4, D6), records it
// and starts its run (D3, D5, D9), and moves it on, all in one transaction.
func (f *Firer) fireOne(ctx context.Context) (bool, uuid.UUID, error) {
	var claimed uuid.UUID
	var run uuid.UUID
	now := f.now()
	err := f.store.InTx(ctx, func(q *db.Queries) error {
		row, err := q.ClaimDueSchedule(ctx, now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim a due schedule: %w", err)
		}
		claimed = row.ID

		rule, err := when.Parse(row.Kind, row.Rule, row.Timezone, row.Anchor)
		if err != nil {
			// A rule that no longer reads (a zone gone from the database):
			// it cannot fire, and saying so is better than trying each minute.
			f.logger.ErrorContext(ctx, "A schedule's rule does not read; pausing it", "schedule_id", row.ID, "error", err)
			row.Status, row.PausedReason, row.NextFireAt = Paused, ptr(Failing), nil
			return save(ctx, q, row)
		}
		lastLocal := ""
		if row.LastLocal != nil {
			lastLocal = *row.LastLocal
		}
		plan := when.Due(rule, *row.NextFireAt, now, lastLocal)

		for _, t := range plan.Missed {
			if _, err := q.InsertScheduleFire(ctx, db.InsertScheduleFireParams{
				ScheduleID: row.ID, TeamID: row.TeamID, ScheduledFor: t, FiredAt: now, Outcome: string(when.Missed),
			}); err != nil {
				return fmt.Errorf("record a missed fire: %w", err)
			}
		}

		if plan.Fire != nil {
			row.LastFiredAt, row.LastLocal = &now, &plan.Fire.Local
			l, err := LimitsOf(q.GetTeamSettings(ctx, row.TeamID))
			if err != nil {
				return err
			}
			st, err := start(ctx, q, row, l, *plan.Fire, now, false)
			if err != nil {
				return err
			}
			run = st.run
			if st.pause != "" {
				row.Status, row.PausedReason, row.NextFireAt = Paused, &st.pause, nil
				return save(ctx, q, row)
			}
		}

		switch {
		case plan.Done:
			row.Status, row.NextFireAt = Done, nil
		default:
			row.NextFireAt = &plan.Next
		}
		return save(ctx, q, row)
	})
	if err != nil && claimed != uuid.Nil {
		return false, uuid.Nil, &fireFailed{id: claimed, err: err}
	}
	if err != nil {
		return false, uuid.Nil, err
	}
	return claimed != uuid.Nil, run, nil
}

func save(ctx context.Context, q *db.Queries, row db.Schedule) error {
	if _, err := q.SaveSchedule(ctx, saveOf(row)); err != nil {
		return fmt.Errorf("move schedule %s on: %w", row.ID, err)
	}
	return nil
}

// started is what came of starting a fire: its outcome as recorded, the run
// it started if any, and a reason to pause. already is a time that had fired,
// which starts and records nothing.
type started struct {
	outcome, pause string
	run            uuid.UUID
	already        bool
}

// start runs the checks a fire passes, in order, and records the first that
// fails as the fire's outcome with no run: the maker may still start runs
// (D9), its agent is still there, the team is under its hourly cap (D7), the
// schedule's last run has finished (D5). Then it writes the fire before the run, so a time already
// fired starts nothing, and creates the run. now is to the second, so a run
// asked for now is its own time. A refusal asked for now records nothing.
func start(ctx context.Context, q *db.Queries, row db.Schedule, l Limits, slot when.Slot, now time.Time, asked bool) (started, error) {
	// A fire that a check stopped, recorded with no run.
	record := func(outcome string) error {
		if asked {
			return nil
		}
		if _, err := q.InsertScheduleFire(ctx, db.InsertScheduleFireParams{
			ScheduleID: row.ID, TeamID: row.TeamID, ScheduledFor: slot.ScheduledFor, FiredAt: now, Outcome: outcome,
		}); err != nil {
			return fmt.Errorf("record the fire: %w", err)
		}
		return nil
	}

	if row.CreatedBy == nil {
		return started{outcome: skippedMaker, pause: MakerGone}, record(skippedMaker)
	}
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: row.TeamID, UserID: *row.CreatedBy, Action: runWrite})
	if err != nil {
		return started{}, fmt.Errorf("check the maker may start runs: %w", err)
	}
	if !may {
		return started{outcome: skippedMaker, pause: MakerCannotRun}, record(skippedMaker)
	}

	// An agent deleted leaves neither it nor the hub; one still there is held
	// until the run names it.
	if !row.Hub && row.AgentID == nil {
		return started{outcome: skippedTarget, pause: TargetGone}, record(skippedTarget)
	}
	if !row.Hub {
		if _, err := q.HoldAgent(ctx, db.HoldAgentParams{ID: *row.AgentID, TeamID: row.TeamID}); errors.Is(err, pgx.ErrNoRows) {
			return started{outcome: skippedTarget, pause: TargetGone}, record(skippedTarget)
		} else if err != nil {
			return started{}, fmt.Errorf("hold the schedule's agent: %w", err)
		}
	}

	if err := q.LockTeamTriggers(ctx, row.TeamID); err != nil {
		return started{}, fmt.Errorf("lock the team's schedules: %w", err)
	}
	ran, err := q.CountTriggerRunsInHour(ctx, db.CountTriggerRunsInHourParams{TeamID: row.TeamID, Now: now})
	if err != nil {
		return started{}, fmt.Errorf("count the team's runs this hour: %w", err)
	}
	if int(ran) >= l.FiresPerHour {
		return started{outcome: skippedCap}, record(skippedCap)
	}

	session, err := q.GetTriggerSessionOfSchedule(ctx, &row.ID)
	if err != nil {
		return started{}, fmt.Errorf("read the schedule's conversation: %w", err)
	}
	if _, err := q.ActiveSessionRun(ctx, session.ID); err == nil {
		return started{outcome: skippedBusy}, record(skippedBusy)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return started{}, fmt.Errorf("check the schedule's last run: %w", err)
	}

	fresh, err := q.InsertScheduleFire(ctx, db.InsertScheduleFireParams{
		ScheduleID: row.ID, TeamID: row.TeamID, ScheduledFor: slot.ScheduledFor, FiredAt: now,
		Outcome: string(slot.Outcome),
	})
	if err != nil {
		return started{}, fmt.Errorf("record the fire: %w", err)
	}
	if fresh == 0 {
		return started{already: true}, nil
	}
	id := uuid.New()
	if _, err := q.CreateTriggeredRun(ctx, db.CreateTriggeredRunParams{
		ID: id, TeamID: row.TeamID, SessionID: session.ID, UserID: *row.CreatedBy,
		AgentID: row.AgentID, Hub: row.Hub, Input: Input(row, slot),
	}); err != nil {
		return started{}, fmt.Errorf("start the schedule's run: %w", err)
	}
	if err := q.SetFireRun(ctx, db.SetFireRunParams{RunID: &id, ScheduleID: row.ID, ScheduledFor: slot.ScheduledFor}); err != nil {
		return started{}, fmt.Errorf("record the fire's run: %w", err)
	}
	return started{outcome: string(slot.Outcome), run: id}, nil
}

// Input is what a fired run is asked: which schedule and when, what to do,
// and how to say there is nothing to say. The objective is the person's own
// words, but is wrapped all the same: one an agent worded may hold text from
// a page it read.
func Input(row db.Schedule, slot when.Slot) string {
	zone := zoneOr(row.Timezone)
	due := slot.ScheduledFor.In(zone).Format("Mon 2 Jan 2006 15:04") + " " + row.Timezone
	var b strings.Builder
	fmt.Fprintf(&b, "This is a scheduled run of %q, due at %s", row.Name, due)
	switch slot.Outcome {
	case when.Started, when.Missed:
	case when.Late:
		b.WriteString(", running late")
	case when.CatchUp:
		b.WriteString(", running late: earlier times it was due were missed while the team was not running")
	}
	end := rand.Text()
	fmt.Fprintf(&b, ".\nDo what the objective asks. If there is nothing worth telling the person, answer exactly %s and nothing else.\n\n", store.Silent)
	fmt.Fprintf(&b, "<objective end=\"%s\">\n%s\n</objective end=\"%s\">", end, unclose(row.Objective), end)
	return b.String()
}

// unclose keeps an objective from ending its own wrapper early.
func unclose(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "</objective", "&lt;/objective"), "<objective", "&lt;objective")
}
