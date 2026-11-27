package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

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
	// ReadOutside is whether the run has called a tool other than these
	// two and plan since it was built: what such a tool returned may say
	// to confirm, so a confirm must come first.
	ReadOutside func() bool
}

// readsNothing are the tools a run may call before it confirms: their
// answers are its own proposals and notes, not what someone else wrote.
var readsNothing = append([]string{PlanToolName}, ScheduleToolNames...)

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
	read := &atomic.Bool{}
	r := SchedulingRun{
		Team: run.TeamID, User: *run.UserID, Run: run.ID, Session: run.SessionID,
		Agent:       func(ctx context.Context, name string) (uuid.UUID, error) { return b.schedulable(ctx, a, run, name) },
		ReadOutside: read.Load,
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
	built.outside = read
	return nil
}

// noteReads has every tool the run holds but those that read nothing mark
// the run as having read, once Build has given it all of them: found ones
// too, which the graph hands it from Discovery.
func (built *Built) noteReads() {
	if built.outside == nil {
		return
	}
	for i, t := range built.Spec.Tools {
		if !slices.Contains(readsNothing, t.Name) {
			built.Spec.Tools[i] = marking(t, built.outside)
		}
	}
	if d := built.Spec.Discovery; d != nil {
		for i, t := range d.Tools {
			d.Tools[i] = marking(t, built.outside)
		}
	}
}

// marking is t, marking read when it is called.
func marking(t agent.Tool, read *atomic.Bool) agent.Tool {
	if invoke := t.Invoke; invoke != nil {
		t.Invoke = func(ctx context.Context, args json.RawMessage) (string, error) {
			read.Store(true)
			return invoke(ctx, args)
		}
	}
	if result := t.Result; result != nil {
		t.Result = func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
			read.Store(true)
			return result(ctx, args)
		}
	}
	return t
}

// schedulable is an agent a schedule of this run's may run: the one talking,
// or one it may hand work to, as delegate would.
func (b *Builder) schedulable(ctx context.Context, caller db.Agent, run db.Run, name string) (uuid.UUID, error) {
	if strings.EqualFold(name, caller.Name) && caller.ID != uuid.Nil {
		return caller.ID, nil
	}
	if b.delegates == nil {
		return uuid.Nil, errNotSchedulable("this run may name no other agent; leave agent out to run it yourself")
	}
	target, err := b.delegates.Store.GetAgentByName(ctx, db.GetAgentByNameParams{TeamID: run.TeamID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, errNotSchedulable(fmt.Sprintf("no agent in this team is named %q", name))
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("looking up the agent %q: %w", name, err)
	}
	if target.ID == caller.ID {
		return target.ID, nil
	}
	if err := reaches(ctx, b.delegates.Store, caller, target); err != nil {
		return uuid.Nil, errNotSchedulable(err.Error())
	}
	return target.ID, nil
}

// NotSchedulable is an agent a run may not name, said for the model; any
// other error from Agent is a failure.
type NotSchedulable struct{ Msg string }

func (e *NotSchedulable) Error() string { return e.Msg }

func errNotSchedulable(msg string) error { return &NotSchedulable{Msg: msg} }

// Enqueue hands a run started on this executor's behalf, such as a schedule
// run now from a chat, to the workers; before the job registers, the sweep
// starts it within a minute.
func (e *Executor) Enqueue(_ context.Context, id uuid.UUID) error {
	if e.enqueue == nil {
		return nil
	}
	return e.enqueue(id, 0)
}
