package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/when"
)

// The states a schedule is in.
const (
	Proposed = "proposed"
	Active   = "active"
	Paused   = "paused"
	Done     = "done"
	Deleted  = "deleted"
)

// Why a schedule paused.
const (
	ByPerson       = "by_person"
	Failing        = "failing"
	MakerCannotRun = "maker_cannot_run"
	MakerGone      = "maker_gone"
	// TargetGone is a schedule whose agent was deleted: it pauses rather than
	// fall to the hub, which nobody chose.
	TargetGone = "target_gone"
)

const (
	maxName      = 80
	maxObjective = 4000
	upcoming     = 3
)

// When is a time rule as asked for: one of at, every or cron, by Kind.
type When struct {
	Kind  when.Kind
	At    time.Time
	Every time.Duration
	Cron  string
}

// ScheduleInput is a new schedule. An empty Timezone is the team's; a nil
// AgentID is the hub; a zero Target is its own conversation.
type ScheduleInput struct {
	Name, Objective string
	When            When
	Timezone        string
	AgentID         *uuid.UUID
	Target          Target
}

// SchedulePatch changes what it sets, all at once or none of it. Hub sets the
// agent back to the hub; Status pauses or resumes. What it does (name,
// objective, rule, agent) is the maker's alone to change, as it runs as them;
// one who manages everyone's may pause and resume it.
type SchedulePatch struct {
	Name, Objective *string
	When            *When
	Timezone        *string
	AgentID         *uuid.UUID
	Hub             bool
	Status          *string
}

// changesWhatItDoes is a patch that sets more than the status.
func (p SchedulePatch) changesWhatItDoes() bool {
	return p.Name != nil || p.Objective != nil || p.When != nil || p.Timezone != nil || p.AgentID != nil || p.Hub
}

// Ref picks a schedule by id, or by name among the caller's own.
type Ref struct {
	ID   uuid.UUID
	Name string
}

// A ScheduleView is a schedule as it is shown.
type ScheduleView struct {
	db.Schedule
	SessionID uuid.UUID
	// SessionChannelID is the channel its conversation is on.
	SessionChannelID *uuid.UUID
	LastOutcome      string
	Upcoming         []time.Time
}

// Preview is the next fires of a rule and the zone it reads them in, before
// anything is saved: what a person is shown, or an agent says, to confirm.
func (s *Service) Preview(ctx context.Context, who Who, w When, zone string) ([]time.Time, string, error) {
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return nil, "", err
	}
	if zone == "" {
		zone = l.Timezone
	}
	now := s.now()
	rule, err := ruleFor(w, zone, now, l.MinInterval)
	if err != nil {
		return nil, "", err
	}
	return rule.Upcoming(now, upcoming), zone, nil
}

// CreateSchedule saves a new schedule and its conversation. Made by a person
// it is active at once; proposed by an agent's tool, it waits for the
// person's next message to confirm it (D10).
func (s *Service) CreateSchedule(ctx context.Context, who Who, origin Origin, in ScheduleInput) (ScheduleView, error) {
	name, objective, err := named(in.Name, in.Objective)
	if err != nil {
		return ScheduleView{}, err
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return ScheduleView{}, err
	}
	zone := in.Timezone
	if zone == "" {
		zone = l.Timezone
	}
	now := s.now()
	rule, err := ruleFor(in.When, zone, now, l.MinInterval)
	if err != nil {
		return ScheduleView{}, err
	}
	first, ok := rule.Next(now)
	if !ok {
		return ScheduleView{}, refuse(Invalid, "the schedule never fires")
	}
	// What needs only the database is checked before the platform is asked;
	// the limits are checked again under the team's lock.
	if err := s.store.InTx(ctx, func(q *db.Queries) error {
		if err := agentInTeam(ctx, q, who.Team, in.AgentID); err != nil {
			return err
		}
		if err := mayRun(ctx, q, who.Team, who.User, Forbidden,
			"you may not start runs in this team, so a schedule of yours could not run"); err != nil {
			return err
		}
		return underLimits(ctx, q, who.Team, who.User, l)
	}); err != nil {
		return ScheduleView{}, err
	}
	// Where it answers is checked, and looked up on its platform, before the
	// transaction: a direct conversation is opened by asking Slack.
	at, err := s.place(ctx, who, in.Target)
	if err != nil {
		return ScheduleView{}, err
	}

	id := uuid.New()
	err = s.store.InTx(ctx, func(q *db.Queries) error {
		if err := agentInTeam(ctx, q, who.Team, in.AgentID); err != nil {
			return err
		}
		if err := mayRun(ctx, q, who.Team, who.User, Forbidden,
			"you may not start runs in this team, so a schedule of yours could not run"); err != nil {
			return err
		}
		if err := underLimits(ctx, q, who.Team, who.User, l); err != nil {
			return err
		}
		session := db.CreateTriggerSessionParams{
			TeamID: who.Team, ProviderRef: "trigger:" + id.String(), Kind: string(gateway.SessionDirect), UserID: &who.User, ScheduleID: &id,
		}
		target, label := TargetOwn, ""
		if at != nil {
			session.ChannelID, session.ProviderRef, session.Kind, session.UserID = at.channel, at.ref, at.kind, at.user
			target, label = at.target, at.label
		} else {
			ch, err := q.DashboardChannel(ctx, who.Team)
			if err != nil {
				return fmt.Errorf("read the dashboard channel: %w", err)
			}
			session.ChannelID = ch.ID
		}
		row := db.CreateScheduleParams{
			ID: id, TeamID: who.Team, CreatedBy: who.User, AgentID: in.AgentID, Hub: in.AgentID == nil,
			Name: name, Objective: objective, Kind: string(rule.Kind), Rule: rule.Stored(),
			Anchor: rule.Anchor, Timezone: zone, Status: Active, NextFireAt: &first,
			Target: target, TargetLabel: label,
		}
		if origin.viaTool() {
			row.Status, row.NextFireAt = Proposed, nil
			row.ProposedRun, row.ProposedIn = &origin.Run, &origin.Session
		}
		if _, err := q.CreateSchedule(ctx, row); err != nil {
			return nameTaken(err, name)
		}
		if _, err := q.CreateTriggerSession(ctx, session); err != nil {
			return fmt.Errorf("start the schedule's conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// Schedule is one the caller may see.
func (s *Service) Schedule(ctx context.Context, who Who, ref Ref) (ScheduleView, error) {
	id, err := s.resolve(ctx, who, ref)
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// ListSchedules is the caller's own, newest first, or everyone's for one who
// may manage them. No statuses is every one not deleted.
func (s *Service) ListSchedules(ctx context.Context, who Who, everyone bool, statuses []string,
	after *time.Time, afterID *uuid.UUID, limit int32,
) ([]ScheduleView, error) {
	if everyone && !who.Manage {
		return nil, refuse(Forbidden, "seeing everyone's schedules takes schedule:manage")
	}
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := s.store.ListScheduleViews(ctx, db.ListScheduleViewsParams{
		TeamID: who.Team, Everyone: everyone, Viewer: who.User, Statuses: statuses,
		AfterCreated: after, AfterID: afterID, PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list the schedules: %w", err)
	}
	now := s.now()
	out := make([]ScheduleView, len(rows))
	for i, r := range rows {
		out[i] = viewOf(r.Schedule, r.SessionID, r.SessionChannelID, r.LastOutcome, now)
	}
	return out, nil
}

// Fires is the history, newest first, of a schedule the caller may see.
func (s *Service) Fires(ctx context.Context, who Who, ref Ref, before *time.Time, limit int32) ([]db.ScheduleFire, error) {
	id, err := s.resolve(ctx, who, ref)
	if err != nil {
		return nil, err
	}
	// resolve trusts an id as given: whose it is, and which team's, is view's.
	if _, err := s.view(ctx, who, id); err != nil {
		return nil, err
	}
	rows, err := s.store.ListScheduleFires(ctx, db.ListScheduleFiresParams{
		ScheduleID: id, TeamID: who.Team, Before: before, PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list the schedule's fires: %w", err)
	}
	return rows, nil
}

// UpdateSchedule changes what the patch sets, in one transaction. A new rule
// or zone moves the next fire; the conversation it answers in never moves.
func (s *Service) UpdateSchedule(ctx context.Context, who Who, ref Ref, p SchedulePatch) (ScheduleView, error) {
	if p.Status != nil && *p.Status != Active && *p.Status != Paused {
		return ScheduleView{}, refuse(Invalid, "status is active or paused")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return ScheduleView{}, err
	}
	now := s.now()
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if p.changesWhatItDoes() {
			if err := ownsIt(who, *row); err != nil {
				return err
			}
		}
		if err := applyPatch(ctx, q, row, p, l, now); err != nil {
			return err
		}
		switch {
		case p.Status == nil || *p.Status == row.Status:
			return nil
		case *p.Status == Paused:
			return pauseRow(row)
		default:
			return resumeRow(ctx, q, row, now)
		}
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// applyPatch sets what the patch names on row, checked as a create checks it.
func applyPatch(ctx context.Context, q *db.Queries, row *db.Schedule, p SchedulePatch, l Limits, now time.Time) error {
	if p.Name != nil || p.Objective != nil {
		name, objective := row.Name, row.Objective
		if p.Name != nil {
			name = *p.Name
		}
		if p.Objective != nil {
			objective = *p.Objective
		}
		var err error
		if row.Name, row.Objective, err = named(name, objective); err != nil {
			return err
		}
	}
	switch {
	case p.Hub:
		row.AgentID, row.Hub = nil, true
	case p.AgentID != nil:
		if err := agentInTeam(ctx, q, row.TeamID, p.AgentID); err != nil {
			return err
		}
		row.AgentID, row.Hub = p.AgentID, false
	}
	if p.When == nil && p.Timezone == nil {
		return nil
	}
	if row.Status == Done {
		return refuse(Conflict, "this schedule has already run; make a new one")
	}
	zone := row.Timezone
	if p.Timezone != nil {
		zone = *p.Timezone
	}
	var rule when.Rule
	var err error
	if p.When != nil {
		rule, err = ruleFor(*p.When, zone, now, l.MinInterval)
	} else if rule, err = when.Parse(row.Kind, row.Rule, zone, row.Anchor); err == nil {
		err = checked(rule, now, l.MinInterval)
	}
	if err != nil {
		return asInvalid(err)
	}
	row.Kind, row.Rule, row.Anchor, row.Timezone = string(rule.Kind), rule.Stored(), rule.Anchor, zone
	row.LastLocal = nil
	if row.Status == Active {
		next, ok := rule.Next(now)
		if !ok {
			return refuse(Invalid, "the schedule never fires")
		}
		row.NextFireAt = &next
	}
	return nil
}

// Pause stops a schedule until it is resumed. Missed fires are not caught up
// after.
func (s *Service) Pause(ctx context.Context, who Who, ref Ref) (ScheduleView, error) {
	id, err := s.change(ctx, who, ref, func(_ *db.Queries, row *db.Schedule) error {
		return pauseRow(row)
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// Resume starts a paused schedule again from now, as long as its maker may
// still start runs and its agent is still there.
func (s *Service) Resume(ctx context.Context, who Who, ref Ref) (ScheduleView, error) {
	now := s.now()
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		return resumeRow(ctx, q, row, now)
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

func pauseRow(row *db.Schedule) error {
	if row.Status != Active {
		return refuse(Conflict, "only an active schedule can be paused; this one is %s", row.Status)
	}
	row.Status, row.PausedReason, row.NextFireAt = Paused, ptr(ByPerson), nil
	return nil
}

func resumeRow(ctx context.Context, q *db.Queries, row *db.Schedule, now time.Time) error {
	if row.Status != Paused {
		return refuse(Conflict, "only a paused schedule can be resumed; this one is %s", row.Status)
	}
	if row.CreatedBy == nil {
		return refuse(Conflict, "the person who made this schedule has left; an admin can take it over first")
	}
	if err := mayRun(ctx, q, row.TeamID, *row.CreatedBy, Conflict,
		"the person who made this schedule may no longer start runs in this team"); err != nil {
		return err
	}
	if !row.Hub && row.AgentID == nil {
		return refuse(Conflict, "the agent this schedule ran was deleted; choose another, or the hub, first")
	}
	rule, err := when.Parse(row.Kind, row.Rule, row.Timezone, row.Anchor)
	if err != nil {
		return asInvalid(err)
	}
	next, ok := rule.Next(now)
	if !ok {
		return refuse(Conflict, "this schedule's time has passed; give it a new one")
	}
	row.Status, row.PausedReason, row.NextFireAt, row.ConsecutiveFailures = Active, nil, &next, 0
	return nil
}

// Confirm makes a proposal active. From an agent's tool, only a run started by
// a later message from the same person, in the conversation it was proposed
// in, may: text the proposing run read cannot confirm what it proposed.
func (s *Service) Confirm(ctx context.Context, who Who, origin Origin, ref Ref) (ScheduleView, error) {
	now := s.now()
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if row.Status != Proposed {
			return refuse(Conflict, "this schedule is not waiting to be confirmed; it is %s", row.Status)
		}
		if row.CreatedBy == nil || *row.CreatedBy != who.User {
			return refuse(Forbidden, "only the person it was proposed to may confirm it")
		}
		if now.Sub(row.CreatedAt) > proposalLife {
			return refuse(Conflict, "the proposal waited more than an hour and lapsed; ask for it again")
		}
		if origin.viaTool() {
			if err := laterMessage(ctx, q, row, origin); err != nil {
				return err
			}
		}
		rule, err := when.Parse(row.Kind, row.Rule, row.Timezone, row.Anchor)
		if err != nil {
			return asInvalid(err)
		}
		next, ok := rule.Next(now)
		if !ok {
			return refuse(Conflict, "the proposed time has passed; ask for a new one")
		}
		row.Status, row.NextFireAt = Active, &next
		return nil
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// laterMessage holds a confirmation from a tool to the conversation and the
// person the schedule was proposed to, and to a run a later message started.
func laterMessage(ctx context.Context, q *db.Queries, row *db.Schedule, origin Origin) error {
	if row.ProposedRun != nil && *row.ProposedRun == origin.Run {
		return refuse(Conflict, "ask the person; a schedule is confirmed by their next message, not in the run that proposed it")
	}
	if row.ProposedIn == nil || *row.ProposedIn != origin.Session {
		return refuse(Forbidden, "a schedule is confirmed in the conversation it was proposed in")
	}
	run, err := q.GetRun(ctx, origin.Run)
	if err != nil {
		return fmt.Errorf("read the confirming run: %w", err)
	}
	if run.UserID == nil || row.CreatedBy == nil || *run.UserID != *row.CreatedBy || run.MessageID == nil {
		return refuse(Forbidden, "only a message from the person it was proposed to confirms it")
	}
	at, err := q.MessageReceivedAt(ctx, db.MessageReceivedAtParams{ID: *run.MessageID, SessionID: origin.Session})
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse(Forbidden, "only a message from the person it was proposed to confirms it")
	}
	if err != nil {
		return fmt.Errorf("read the confirming message: %w", err)
	}
	if !at.After(row.CreatedAt) {
		return refuse(Forbidden, "only a message sent after the proposal confirms it")
	}
	return nil
}

// Delete marks a schedule deleted and closes its conversation, which stays
// with its runs. A run already working finishes as usual.
func (s *Service) Delete(ctx context.Context, who Who, ref Ref) error {
	now := s.now()
	_, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		row.Status, row.PausedReason, row.NextFireAt, row.DeletedAt = Deleted, nil, nil, &now
		if err := q.CloseTriggerSession(ctx, db.CloseTriggerSessionParams{ScheduleID: &row.ID}); err != nil {
			return fmt.Errorf("close the schedule's conversation: %w", err)
		}
		return nil
	})
	return err
}

// TakeOver gives a schedule whose maker left, or may no longer start runs, to
// the admin asking. It stays paused until they resume it.
func (s *Service) TakeOver(ctx context.Context, who Who, ref Ref) (ScheduleView, error) {
	if !who.Manage {
		return ScheduleView{}, refuse(Forbidden, "taking over a schedule takes schedule:manage")
	}
	id, err := s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if row.Status != Paused || row.PausedReason == nil ||
			(*row.PausedReason != MakerGone && *row.PausedReason != MakerCannotRun) {
			return refuse(Conflict, "only a schedule paused because its maker left or may no longer run can be taken over")
		}
		ss, err := q.GetTriggerSessionOfSchedule(ctx, &row.ID)
		if err != nil {
			return fmt.Errorf("read the schedule's conversation: %w", err)
		}
		// Its answers would still reach the person who left, in their own
		// direct messages or their own thread.
		if row.Target == TargetDirect || (row.Target == TargetChat && !postsToGroup(row.Target, ss.Kind)) {
			return refuse(Conflict, "this schedule answers in its maker's direct messages or their own thread, "+
				"so it cannot be handed on; delete it and make another")
		}
		// It runs as whoever takes it, so they must be able to run, and to
		// post where a group reads it.
		if err := mayRun(ctx, q, row.TeamID, who.User, Forbidden,
			"you may not start runs in this team, so a schedule taken over by you could not run"); err != nil {
			return err
		}
		if postsToGroup(row.Target, ss.Kind) && !who.ChannelWrite {
			return refuse(Forbidden, "this schedule posts where a group reads it, which takes channel:write")
		}
		row.CreatedBy = &who.User
		if err := q.SetTriggerSessionUser(ctx, db.SetTriggerSessionUserParams{UserID: who.User, ScheduleID: row.ID}); err != nil {
			return fmt.Errorf("hand over the schedule's conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return ScheduleView{}, err
	}
	return s.view(ctx, who, id)
}

// RunNow fires a schedule once, now, as an extra time: under the same checks
// as a fire on the clock, said back as a refusal rather than recorded.
func (s *Service) RunNow(ctx context.Context, who Who, ref Ref) (uuid.UUID, error) {
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return uuid.Nil, err
	}
	now := s.now().Truncate(time.Second)
	var run uuid.UUID
	_, err = s.change(ctx, who, ref, func(q *db.Queries, row *db.Schedule) error {
		if err := ownsIt(who, *row); err != nil {
			return err
		}
		if row.Status == Proposed {
			return refuse(Conflict, "confirm this schedule before running it")
		}
		slot := when.Slot{ScheduledFor: now, Outcome: when.Started, Local: now.In(zoneOr(row.Timezone)).Format(when.LocalLayout)}
		st, err := start(ctx, q, *row, l, slot, now, true)
		if err != nil {
			return err
		}
		switch {
		case st.already:
			return refuse(Conflict, "this schedule was just run; try again in a moment")
		case st.outcome == skippedMaker:
			return refuse(Conflict, "the person who made this schedule may no longer start runs in this team, "+
				"or post where it answers")
		case st.outcome == skippedTarget:
			return refuse(Conflict, "the agent this schedule ran was deleted; choose another, or the hub, first")
		case st.outcome == skippedCap:
			return refuse(Limit, "this team's schedules and watchers have started %d runs in the last hour, which is its limit", l.FiresPerHour)
		case st.outcome == skippedBusy:
			return refuse(Conflict, "the schedule's last run is still working; try again when it has finished")
		}
		run = st.run
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.runs.Enqueue(ctx, run); err != nil {
		s.logger.WarnContext(ctx, "A schedule's run was not enqueued; the runtime's sweep will start it", "run_id", run, "error", err)
	}
	return run, nil
}

// News is what the caller has not yet seen of their own schedules.
func (s *Service) News(ctx context.Context, who Who, limit int32) ([]db.ListScheduleNewsRow, error) {
	rows, err := s.store.ListScheduleNews(ctx, db.ListScheduleNewsParams{TeamID: who.Team, Maker: who.User, PageSize: limit})
	if err != nil {
		return nil, fmt.Errorf("read the schedules' news: %w", err)
	}
	return rows, nil
}

// Seen marks everything one of the caller's schedules said as seen.
func (s *Service) Seen(ctx context.Context, who Who, schedule uuid.UUID) error {
	if _, err := s.store.MarkScheduleSeen(ctx, db.MarkScheduleSeenParams{
		ScheduleID: schedule, TeamID: who.Team, Maker: who.User,
	}); err != nil {
		return fmt.Errorf("mark the schedule seen: %w", err)
	}
	return nil
}

// resolve is the id a ref names, among what the caller may see.
func (s *Service) resolve(ctx context.Context, who Who, ref Ref) (uuid.UUID, error) {
	if ref.ID != uuid.Nil {
		return ref.ID, nil
	}
	id, err := s.store.ScheduleIDByName(ctx, db.ScheduleIDByNameParams{TeamID: who.Team, Maker: who.User, Name: ref.Name})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, refuse(NotFound, "you have no schedule named %q", ref.Name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find the schedule %q: %w", ref.Name, err)
	}
	return id, nil
}

// change locks a schedule the caller may change, lets fn change it, and
// writes it back, all in one transaction.
func (s *Service) change(ctx context.Context, who Who, ref Ref, fn func(*db.Queries, *db.Schedule) error) (uuid.UUID, error) {
	id, err := s.resolve(ctx, who, ref)
	if err != nil {
		return uuid.Nil, err
	}
	err = s.store.InTx(ctx, func(q *db.Queries) error {
		row, err := q.LockSchedule(ctx, db.LockScheduleParams{ID: id, TeamID: who.Team})
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(NotFound, "no such schedule")
		}
		if err != nil {
			return fmt.Errorf("lock the schedule: %w", err)
		}
		if !mine(who, row) {
			return refuse(NotFound, "no such schedule")
		}
		if err := fn(q, &row); err != nil {
			return err
		}
		if _, err := q.SaveSchedule(ctx, saveOf(row)); err != nil {
			return nameTaken(err, row.Name)
		}
		return nil
	})
	return id, err
}

func (s *Service) view(ctx context.Context, who Who, id uuid.UUID) (ScheduleView, error) {
	row, err := s.store.GetScheduleView(ctx, db.GetScheduleViewParams{ID: id, TeamID: who.Team})
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduleView{}, refuse(NotFound, "no such schedule")
	}
	if err != nil {
		return ScheduleView{}, fmt.Errorf("read the schedule: %w", err)
	}
	if !mine(who, row.Schedule) {
		return ScheduleView{}, refuse(NotFound, "no such schedule")
	}
	return viewOf(row.Schedule, row.SessionID, row.SessionChannelID, row.LastOutcome, s.now()), nil
}

func viewOf(row db.Schedule, session uuid.UUID, channel *uuid.UUID, last string, now time.Time) ScheduleView {
	v := ScheduleView{Schedule: row, SessionID: session, SessionChannelID: channel, LastOutcome: last}
	if row.Status != Active && row.Status != Proposed {
		return v
	}
	if rule, err := when.Parse(row.Kind, row.Rule, row.Timezone, row.Anchor); err == nil {
		v.Upcoming = rule.Upcoming(now, upcoming)
	}
	return v
}

// mine is a schedule the caller made, or one they may manage.
func mine(who Who, row db.Schedule) bool {
	return who.Manage || made(who, row)
}

func made(who Who, row db.Schedule) bool {
	return row.CreatedBy != nil && *row.CreatedBy == who.User
}

// ownsIt holds what a schedule does, and running it, to its maker: its runs
// are theirs, with their permissions, so nobody else words or starts them.
func ownsIt(who Who, row db.Schedule) error {
	if made(who, row) {
		return nil
	}
	return refuse(Forbidden, "only the person who made this schedule may change what it does or run it")
}

// ruleFor reads a rule as asked: an interval's first fire is one interval
// from the minute it was asked.
func ruleFor(w When, zone string, now time.Time, minInterval time.Duration) (when.Rule, error) {
	var stored string
	anchor := now
	switch w.Kind {
	case when.At:
		if w.At.IsZero() {
			return when.Rule{}, refuse(Invalid, "at needs a time")
		}
		stored = w.At.UTC().Format(time.RFC3339)
	case when.Every:
		stored = fmt.Sprint(int64(w.Every / time.Second))
		anchor = now.Truncate(time.Minute).Add(w.Every)
	case when.Cron:
		stored = w.Cron
	default:
		return when.Rule{}, refuse(Invalid, "a schedule is at a time, every interval, or a cron rule")
	}
	rule, err := when.Parse(string(w.Kind), stored, zone, anchor)
	if err != nil {
		return when.Rule{}, asInvalid(err)
	}
	if err := checked(rule, now, minInterval); err != nil {
		return when.Rule{}, err
	}
	return rule, nil
}

func checked(r when.Rule, now time.Time, minInterval time.Duration) error {
	if err := r.Check(now, minInterval); err != nil {
		return asInvalid(err)
	}
	return nil
}

func asInvalid(err error) error {
	if KindOf(err) != 0 {
		return err
	}
	return &Error{Kind: Invalid, Msg: err.Error()}
}

func named(name, objective string) (string, string, error) {
	name, objective = strings.TrimSpace(name), strings.TrimSpace(objective)
	switch {
	case name == "":
		return "", "", refuse(Invalid, "a schedule needs a name")
	case utf8.RuneCountInString(name) > maxName:
		return "", "", refuse(Invalid, "a name is at most %d characters", maxName)
	case objective == "":
		return "", "", refuse(Invalid, "a schedule needs an objective: what to do each time it runs")
	case utf8.RuneCountInString(objective) > maxObjective:
		return "", "", refuse(Invalid, "an objective is at most %d characters", maxObjective)
	case strings.ContainsRune(name+objective, 0):
		return "", "", refuse(Invalid, "a name or objective must not contain a NUL character")
	}
	return name, objective, nil
}

func agentInTeam(ctx context.Context, q *db.Queries, team uuid.UUID, agent *uuid.UUID) error {
	if agent == nil {
		return nil
	}
	a, err := q.GetAgent(ctx, *agent)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.TeamID != team) {
		return refuse(Invalid, "no such agent in this team")
	}
	if err != nil {
		return fmt.Errorf("read the agent: %w", err)
	}
	return nil
}

func nameTaken(err error, name string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "schedules_name_key" {
		return refuse(Conflict, "you already have a schedule named %q", name)
	}
	return fmt.Errorf("save the schedule: %w", err)
}

func saveOf(r db.Schedule) db.SaveScheduleParams {
	return db.SaveScheduleParams{
		ID: r.ID, CreatedBy: r.CreatedBy, AgentID: r.AgentID, Hub: r.Hub, Name: r.Name, Objective: r.Objective,
		Kind: r.Kind, Rule: r.Rule, Anchor: r.Anchor, Timezone: r.Timezone, Status: r.Status,
		PausedReason: r.PausedReason, DeletedAt: r.DeletedAt, NextFireAt: r.NextFireAt,
		LastFiredAt: r.LastFiredAt, LastLocal: r.LastLocal, ConsecutiveFailures: r.ConsecutiveFailures,
	}
}

func zoneOr(name string) *time.Location {
	if loc, err := when.Zone(name); err == nil {
		return loc
	}
	return time.UTC
}
