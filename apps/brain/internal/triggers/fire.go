package triggers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// The outcomes a fire is recorded with, beside when's.
const (
	skippedBusy  = "skipped_busy"
	skippedCap   = "skipped_cap"
	skippedMaker = "skipped_maker"
)

// FireStore is what firing reads and writes, in one transaction a schedule.
type FireStore interface {
	InTx(ctx context.Context, fn func(*db.Queries) error) error
	GetTeamSettings(ctx context.Context, teamID uuid.UUID) (db.TeamSetting, error)
	CountDueSchedules(ctx context.Context, now time.Time) (int32, error)
}

// A Firer fires the schedules that are due, one transaction each.
type Firer struct {
	store  FireStore
	runs   Enqueuer
	logger *slog.Logger
	now    func() time.Time
}

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
// its transaction commits. Another worker doing the same takes other rows.
func (f *Firer) FireDue(ctx context.Context, maxFires int, budget time.Duration) (int, error) {
	deadline := f.now().Add(budget)
	fired := 0
	for fired < maxFires && f.now().Before(deadline) {
		claimed, run, err := f.fireOne(ctx)
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

// fireOne takes one due schedule, works out what is due (D4, D6), records it
// and starts its run (D3, D5, D9), and moves it on, all in one transaction.
func (f *Firer) fireOne(ctx context.Context) (bool, uuid.UUID, error) {
	var claimed bool
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
		claimed = true

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
	if err != nil {
		return false, uuid.Nil, err
	}
	return claimed, run, nil
}

func save(ctx context.Context, q *db.Queries, row db.Schedule) error {
	if _, err := q.SaveSchedule(ctx, saveOf(row)); err != nil {
		return fmt.Errorf("move schedule %s on: %w", row.ID, err)
	}
	return nil
}

// started is what came of starting a fire: its outcome as recorded, the run
// it started if any, and a reason to pause. An empty outcome is a time
// already fired.
type started struct {
	outcome, pause string
	run            uuid.UUID
}

// start runs the checks a fire passes, in order, and records the first that
// fails as the fire's outcome with no run: the maker may still start runs
// (D9), the team is under its hourly cap (D7), the schedule's last run has
// finished (D5). Then it writes the fire before the run, so a time already
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
		return started{}, nil
	}
	id := uuid.New()
	if _, err := q.CreateTriggeredRun(ctx, db.CreateTriggeredRunParams{
		ID: id, TeamID: row.TeamID, SessionID: session.ID, UserID: *row.CreatedBy,
		AgentID: row.AgentID, Input: Input(row, slot),
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
