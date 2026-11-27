package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// ScheduleToolNames are the tools Scheduling hands a run, named here so a
// run without them can say why.
var ScheduleToolNames = []string{"schedule", "watch"}

// Scheduling hands a run the schedule and watch tools, which propose runs at
// a time or on a change for the person to confirm. Its rules live outside
// the runtime; it decides whether this person, team and conversation may.
type Scheduling interface {
	// Tools are the tools for r, or why it has none.
	Tools(ctx context.Context, r SchedulingRun) ([]agent.Tool, string, error)
	// StandIns are the tools as a replay holds them: named and described,
	// and never called.
	StandIns() []agent.Tool
}

// SchedulingRun is a run that may hold the tools.
type SchedulingRun struct {
	Team, User, Run, Session uuid.UUID
	// Self is the agent talking, nil for the hub.
	Self *uuid.UUID
	// Agent finds an agent the run may hand work to, by name.
	Agent func(ctx context.Context, name string) (uuid.UUID, error)
}

// WithScheduling gives a top-level run of the hub, or of an agent granted
// it, the schedule and watch tools (D10).
func (b *Builder) WithScheduling(s Scheduling) *Builder {
	b.scheduling = s
	return b
}

// schedulingFor is the run's schedule and watch tools: only a top-level run
// a person is there for, of the hub or an agent granted them. The builder's
// is neither: it is never the hub, and its agent holds no grant. A run a
// schedule or watcher started gets neither; Scheduling says so.
func (b *Builder) schedulingFor(ctx context.Context, run db.Run, a db.Agent, built *Built) error {
	if b.scheduling == nil || run.ParentRunID != nil || run.UserID == nil || (!run.Hub && !a.Schedule) {
		return nil
	}
	if b.replay {
		// What a call would do has moved on since: a stand-in, not made.
		for _, t := range b.scheduling.StandIns() {
			built.Spec.Tools = append(built.Spec.Tools, refusing(t, notMade))
		}
		return nil
	}
	r := SchedulingRun{
		Team: run.TeamID, User: *run.UserID, Run: run.ID, Session: run.SessionID,
		Agent: func(ctx context.Context, name string) (uuid.UUID, error) { return b.schedulable(ctx, a, run, name) },
	}
	// The hub's schedule runs the hub, whichever agent the team made it.
	if a.ID != uuid.Nil && !run.Hub {
		r.Self = &a.ID
	}
	tools, why, err := b.scheduling.Tools(ctx, r)
	if err != nil {
		return fmt.Errorf("reading whether the run may schedule: %w", err)
	}
	if why != "" {
		for _, n := range ScheduleToolNames {
			built.Unavailable = append(built.Unavailable, Unavailable{Name: n, Reason: why})
		}
		return nil
	}
	built.Spec.Tools = append(built.Spec.Tools, tools...)
	return nil
}

// schedulable is an agent a schedule of this run's may run: the one talking,
// or one it may hand work to, as delegate would.
func (b *Builder) schedulable(ctx context.Context, caller db.Agent, run db.Run, name string) (uuid.UUID, error) {
	if b.delegates == nil {
		return uuid.Nil, errors.New("this run may name no other agent; leave agent out to run it yourself")
	}
	target, err := b.delegates.Store.GetAgentByName(ctx, db.GetAgentByNameParams{TeamID: run.TeamID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("no agent in this team is named %q", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("looking up the agent %q: %w", name, err)
	}
	if target.ID == caller.ID {
		return target.ID, nil
	}
	if err := reaches(ctx, b.delegates.Store, caller, target); err != nil {
		return uuid.Nil, err
	}
	return target.ID, nil
}

// Enqueue hands a run started on this executor's behalf, such as a schedule
// run now from a chat, to the workers; before the job registers, the sweep
// starts it within a minute.
func (e *Executor) Enqueue(_ context.Context, id uuid.UUID) error {
	if e.enqueue == nil {
		return nil
	}
	return e.enqueue(id, 0)
}
