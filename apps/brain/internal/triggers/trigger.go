package triggers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// A trigger is what the rules shared by schedules and watchers read of
// either: whose it is, where it answers and why it paused. One set of rules
// for both, so "may this maker still post here" cannot drift between them.
type trigger struct {
	id, team     uuid.UUID
	maker        *uuid.UUID
	agent        *uuid.UUID
	hub          bool
	target       string
	status       string
	pausedReason *string
	// noun is what it is, as a person reads it: "schedule" or "watcher".
	noun string
}

func scheduleOf(row *db.Schedule) trigger {
	return trigger{
		id: row.ID, team: row.TeamID, maker: row.CreatedBy, agent: row.AgentID, hub: row.Hub, target: row.Target,
		status: row.Status, pausedReason: row.PausedReason, noun: "schedule",
	}
}

func watcherOf(row *db.Watcher) trigger {
	return trigger{
		id: row.ID, team: row.TeamID, maker: row.CreatedBy, agent: row.AgentID, hub: row.Hub, target: row.Target,
		status: row.Status, pausedReason: row.PausedReason, noun: "watcher",
	}
}

// session is the trigger's own conversation.
func (t trigger) session(ctx context.Context, q *db.Queries) (db.Session, error) {
	if t.noun == "watcher" {
		return q.GetTriggerSessionOfWatcher(ctx, &t.id)
	}
	return q.GetTriggerSessionOfSchedule(ctx, &t.id)
}

// postsInGroup is whether a trigger's answers reach a group (postsToGroup),
// reading its conversation when that depends on it. One whose conversation
// is gone is refused: it can answer nowhere.
func postsInGroup(ctx context.Context, q *db.Queries, t trigger) (bool, error) {
	switch t.target {
	case TargetChannel:
		return true, nil
	case TargetChat:
		ss, err := t.session(ctx, q)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, refuse(Conflict, "this %s's conversation is gone; delete it and make another", t.noun)
		}
		if err != nil {
			return false, fmt.Errorf("read the %s's conversation: %w", t.noun, err)
		}
		return postsToGroup(t.target, ss.Kind), nil
	}
	return false, nil
}

// makerMayPost is whether a trigger's maker may still post where it answers:
// anywhere, unless a group reads it, which takes channel:write.
func makerMayPost(ctx context.Context, q *db.Queries, t trigger) (bool, error) {
	group, err := postsInGroup(ctx, q, t)
	if err != nil || !group {
		return !group, err
	}
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: t.team, UserID: *t.maker, Action: channelWrite})
	if err != nil {
		return false, fmt.Errorf("check the maker may post in the channel: %w", err)
	}
	return may, nil
}

// makerStuck is a paused trigger its maker cannot resume: they left, or may no
// longer run or post where it answers. One a person paused counts too once
// its maker lost what it needs, or nobody could ever resume it.
func makerStuck(ctx context.Context, q *db.Queries, t trigger) (bool, error) {
	if t.status != Paused || t.pausedReason == nil {
		return false, nil
	}
	switch *t.pausedReason {
	case MakerGone, MakerCannotRun:
		return true, nil
	case ByPerson: // stuck only if its maker lost what it needs, checked below
	default:
		return false, nil
	}
	if t.maker == nil {
		return true, nil
	}
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: t.team, UserID: *t.maker, Action: runWrite})
	if err != nil {
		return false, fmt.Errorf("check the maker may start runs: %w", err)
	}
	if !may {
		return true, nil
	}
	posts, err := makerMayPost(ctx, q, t)
	return !posts, err
}

// A gate is what a trigger's run is stopped by before it starts, or nothing
// when it may start, with the conversation it starts in.
type gate struct {
	outcome, pause string
	// busy is its last run still working: a schedule skips the time, a
	// watcher holds the change.
	busy    bool
	session db.Session
}

// gateOf runs the checks every run a trigger starts passes, in order, as a
// schedule's fire and a watcher's change both do: the maker may still start
// runs (D9) and post where a group reads it, its agent is still there (and
// is held until the run names it), the team is under its hourly cap (D7),
// and its last run has finished (D5). It takes the team's lock for the cap.
func gateOf(ctx context.Context, q *db.Queries, t trigger, l Limits, now time.Time) (gate, error) {
	if t.maker == nil {
		return gate{outcome: skippedMaker, pause: MakerGone}, nil
	}
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: t.team, UserID: *t.maker, Action: runWrite})
	if err != nil {
		return gate{}, fmt.Errorf("check the maker may start runs: %w", err)
	}
	if !may {
		return gate{outcome: skippedMaker, pause: MakerCannotRun}, nil
	}
	session, err := t.session(ctx, q)
	if err != nil {
		return gate{}, fmt.Errorf("read the %s's conversation: %w", t.noun, err)
	}
	if postsToGroup(t.target, session.Kind) {
		may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: t.team, UserID: *t.maker, Action: channelWrite})
		if err != nil {
			return gate{}, fmt.Errorf("check the maker may post in the channel: %w", err)
		}
		if !may {
			return gate{outcome: skippedMaker, pause: MakerCannotRun}, nil
		}
	}
	// An agent deleted leaves neither it nor the hub; one still there is held
	// until the run names it.
	if !t.hub && t.agent == nil {
		return gate{outcome: skippedTarget, pause: TargetGone}, nil
	}
	if !t.hub {
		if _, err := q.HoldAgent(ctx, db.HoldAgentParams{ID: *t.agent, TeamID: t.team}); errors.Is(err, pgx.ErrNoRows) {
			return gate{outcome: skippedTarget, pause: TargetGone}, nil
		} else if err != nil {
			return gate{}, fmt.Errorf("hold the %s's agent: %w", t.noun, err)
		}
	}
	if err := q.LockTeamTriggers(ctx, t.team); err != nil {
		return gate{}, fmt.Errorf("lock the team's schedules: %w", err)
	}
	ran, err := q.CountTriggerRunsInHour(ctx, db.CountTriggerRunsInHourParams{TeamID: t.team, Now: now})
	if err != nil {
		return gate{}, fmt.Errorf("count the team's runs this hour: %w", err)
	}
	if int(ran) >= l.FiresPerHour {
		return gate{outcome: skippedCap}, nil
	}
	if _, err := q.ActiveSessionRun(ctx, session.ID); err == nil {
		return gate{busy: true, session: session}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return gate{}, fmt.Errorf("check the %s's last run: %w", t.noun, err)
	}
	return gate{session: session}, nil
}
