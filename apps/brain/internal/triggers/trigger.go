package triggers

import (
	"context"
	"errors"
	"fmt"

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
	target       string
	status       string
	pausedReason *string
	noun         string
	watcher      bool
}

func scheduleOf(row *db.Schedule) trigger {
	return trigger{
		id: row.ID, team: row.TeamID, maker: row.CreatedBy, target: row.Target,
		status: row.Status, pausedReason: row.PausedReason, noun: "schedule",
	}
}

func watcherOf(row *db.Watcher) trigger {
	return trigger{
		id: row.ID, team: row.TeamID, maker: row.CreatedBy, target: row.Target,
		status: row.Status, pausedReason: row.PausedReason, noun: "watcher", watcher: true,
	}
}

// session is the trigger's own conversation.
func (t trigger) session(ctx context.Context, q *db.Queries) (db.Session, error) {
	if t.watcher {
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
