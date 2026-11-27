package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// The agent's schedule and watch tools (D10). They act as the person the run
// is for, on their own schedules and watchers alone, and propose rather than
// make: what an agent asks for starts only once a later message of that
// person's confirms it, and what it asks of one that runs (a change, a
// delete, a resume, a run now) waits for the same yes (changes.go). A
// confirm comes before the run reads anything someone else wrote, so text it
// read cannot be the yes. No webhook secret ever passes through them.
//
// tools_schema.go is what the model is told, tools_args.go reads what it
// sends, and tools_says.go is what it is answered.

const (
	ScheduleToolName = "schedule"
	WatchToolName    = "watch"

	scheduleWrite = "schedule:write"
	// toolListed is how many of the person's own a list shows.
	toolListed = 50
	// dashboardProvider is the dashboard's own channel.
	dashboardProvider = "dashboard"
	// planToolName is the runtime's plan tool, which reads nothing.
	planToolName = "plan"
)

// readsNothing are the tools a run may have called before it confirms.
var readsNothing = []string{ScheduleToolName, WatchToolName, planToolName}

// ToolRun is a run that may hold the tools: who it acts for, where, and what
// it may name.
type ToolRun struct {
	Team, User, Run, Session uuid.UUID
	// Self is the agent talking, nil for the hub: what a schedule runs
	// unless it names another.
	Self *uuid.UUID
	// Agent is an agent the run may hand work to, found by its name. An
	// *Error is said to the model; any other error is a failure.
	Agent func(ctx context.Context, name string) (uuid.UUID, error)
	// ReadOutside is whether the run has called a tool other than
	// readsNothing since it was built. The calls it made before a wait are
	// read from what it recorded.
	ReadOutside func() bool
	// Dashboard is the origin the dashboard is served from, for a link.
	Dashboard string
}

// toolRun is a run the tools were built for, with what its checks found.
type toolRun struct {
	ToolRun
	who Who
	// chat is the run's conversation as a place to answer in, or zero on
	// the dashboard, where here is a conversation of its own.
	chat Target
	// channel is the channel the run's conversation is on, for "me".
	channel   uuid.UUID
	dashboard bool
}

func (t toolRun) origin() Origin { return Origin{Run: t.Run, Session: t.Session} }

// Tools are the schedule and watch tools for r, or why it has none: the team
// may not let agents schedule, the person may not, or the run is one a
// schedule or watcher started, which proposes no more of either (D10).
func (s *Service) Tools(ctx context.Context, r ToolRun) ([]agent.Tool, string, error) {
	l, err := s.limits(ctx, r.Team)
	if err != nil {
		return nil, "", err
	}
	if !l.AgentsMaySchedule {
		return nil, "the team's settings do not let agents schedule", nil
	}
	var t toolRun
	var why string
	if err := s.store.InTx(ctx, func(q *db.Queries) error {
		var err error
		t, why, err = probeRun(ctx, q, r)
		return err
	}); err != nil {
		return nil, "", err
	}
	if why != "" {
		return nil, why, nil
	}
	return []agent.Tool{s.scheduleTool(t), s.watchTool(t)}, "", nil
}

// probeRun is what the tools need to know of r's person and conversation, or
// why the run may not hold them.
func probeRun(ctx context.Context, q *db.Queries, r ToolRun) (toolRun, string, error) {
	t := toolRun{ToolRun: r, who: Who{Team: r.Team, User: r.User}, dashboard: true}
	may, err := q.MemberMay(ctx, db.MemberMayParams{TeamID: r.Team, UserID: r.User, Action: scheduleWrite})
	if err != nil {
		return t, "", fmt.Errorf("check the person may schedule: %w", err)
	}
	if !may {
		return t, "the person this run is for may not make schedules (schedule:write)", nil
	}
	if t.who.ChannelWrite, err = q.MemberMay(ctx, db.MemberMayParams{TeamID: r.Team, UserID: r.User, Action: channelWrite}); err != nil {
		return t, "", fmt.Errorf("check the person may post where a group reads: %w", err)
	}
	ss, err := q.GetSession(ctx, r.Session)
	if err != nil {
		return t, "", fmt.Errorf("read the run's conversation: %w", err)
	}
	if ss.ScheduleID != nil || ss.WatcherID != nil {
		return t, "a run a schedule or watcher started proposes no more of either", nil
	}
	// A conversation on no channel, or on the dashboard, has here as a
	// conversation of its own.
	if ss.ChannelID == nil {
		return t, "", nil
	}
	ch, err := q.GetChannel(ctx, *ss.ChannelID)
	if err != nil {
		return t, "", fmt.Errorf("read the run's channel: %w", err)
	}
	t.channel, t.dashboard = ch.ID, ch.Provider == dashboardProvider
	if !t.dashboard {
		t.chat = Target{Kind: TargetChat, SessionID: ss.ID}
	}
	return t, "", nil
}

// StandIns are the two tools as a replay holds them: named and described as
// a run's are. A replay never calls them.
func (s *Service) StandIns() []agent.Tool {
	return []agent.Tool{
		{Name: ScheduleToolName, Description: scheduleToolDescription, Schema: json.RawMessage(scheduleSchema)},
		{Name: WatchToolName, Description: watchToolDescription, Schema: json.RawMessage(watchSchema)},
	}
}

// ops are what one tool does for each action, over schedules or watchers.
type ops struct {
	create func(ctx context.Context, t toolRun, a toolArgs) (string, error)
	list   func(ctx context.Context, t toolRun) (string, error)
	// update holds a change, or revises a proposal, which revised says.
	update func(ctx context.Context, t toolRun, ref Ref, a toolArgs) (name string, revised bool, change string, err error)
	pause  func(ctx context.Context, t toolRun, ref Ref) (string, error)
	// ask holds a delete, resume or run now; deleted is a proposal deleted
	// at once.
	ask     func(ctx context.Context, t toolRun, ref Ref, action string) (name string, deleted bool, err error)
	confirm func(ctx context.Context, t toolRun, ref Ref) (string, error)
}

func (s *Service) scheduleTool(t toolRun) agent.Tool {
	return s.tool(t, ScheduleToolName, scheduleToolDescription, scheduleSchema, s.scheduleOps())
}

func (s *Service) watchTool(t toolRun) agent.Tool {
	return s.tool(t, WatchToolName, watchToolDescription, watchSchema, s.watcherOps())
}

func (s *Service) tool(t toolRun, name, description, schema string, o ops) agent.Tool {
	return agent.Tool{
		Name: name, Description: description, Schema: json.RawMessage(schema),
		Invoke: func(ctx context.Context, raw json.RawMessage) (string, error) {
			a, err := parseToolArgs(raw)
			if err != nil {
				return said("", err)
			}
			return said(s.dispatch(ctx, t, o, a))
		},
	}
}

// dispatch does the action a names.
func (s *Service) dispatch(ctx context.Context, t toolRun, o ops, a toolArgs) (string, error) {
	switch a.Action {
	case "create":
		return o.create(ctx, t, a)
	case "list":
		return o.list(ctx, t)
	}
	ref, err := a.ref()
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "update":
		name, revised, change, err := o.update(ctx, t, ref, a)
		if err != nil {
			return "", err
		}
		if revised {
			return revisedSays(name), nil
		}
		return askedSays(name, "to change "+change), nil
	case "pause":
		return o.pause(ctx, t, ref)
	case askDelete, askResume, askRunNow:
		name, deleted, err := o.ask(ctx, t, ref, a.Action)
		if err != nil {
			return "", err
		}
		if deleted {
			return fmt.Sprintf("Deleted the proposal %q; it never ran.", name), nil
		}
		return askedSays(name, ask{Action: a.Action}.says()), nil
	case "confirm":
		if err := s.confirmable(ctx, t); err != nil {
			return "", err
		}
		return o.confirm(ctx, t, ref)
	}
	return "", refuse(Invalid, "action is create, list, update, pause, resume, delete, run_now or confirm")
}

// confirmable refuses a confirm in a run that has read what someone else
// wrote: a page, a file, a server's answer or another agent's could be the
// text that says to confirm (*user*, 2026-11-27).
func (s *Service) confirmable(ctx context.Context, t toolRun) error {
	read := t.ReadOutside != nil && t.ReadOutside()
	if !read {
		var err error
		if read, err = s.store.RunCalledOtherThan(ctx, db.RunCalledOtherThanParams{RunID: t.Run, Names: readsNothing}); err != nil {
			return fmt.Errorf("read what the run called: %w", err)
		}
	}
	if read {
		return refuse(Conflict, "a confirm comes before anything else in a turn, as what a tool read could be what says to confirm; "+
			"ask the person to reply yes again, and confirm first")
	}
	return nil
}

// said is a refusal passed to the model as the tool's answer, to relay or act
// on; a failure stays an error.
func said(out string, err error) (string, error) {
	var refused *Error
	if errors.As(err, &refused) {
		return "Refused: " + refused.Msg, nil
	}
	return out, err
}

func (s *Service) scheduleOps() ops {
	return ops{
		create: s.toolCreateSchedule,
		list:   s.toolListSchedules,
		update: func(ctx context.Context, t toolRun, ref Ref, a toolArgs) (string, bool, string, error) {
			p, err := s.schedulePatchOf(ctx, t, ref, a)
			if err != nil {
				return "", false, "", err
			}
			v, err := s.ProposeScheduleChange(ctx, t.who, t.origin(), ref, p)
			return v.Name, v.Status == Proposed, patchSays(p), err
		},
		pause: func(ctx context.Context, t toolRun, ref Ref) (string, error) {
			v, err := s.Pause(ctx, t.who, ref)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Paused %q. It runs no more until it is resumed.", v.Name), nil
		},
		ask: func(ctx context.Context, t toolRun, ref Ref, action string) (string, bool, error) {
			v, err := s.ProposeScheduleAction(ctx, t.who, t.origin(), ref, action)
			return v.Name, v.Status == Deleted, err
		},
		confirm: func(ctx context.Context, t toolRun, ref Ref) (string, error) {
			v, err := s.Confirm(ctx, t.who, t.origin(), ref)
			if err != nil {
				return "", err
			}
			if v.Status == Deleted {
				return fmt.Sprintf("Deleted %q. Its conversation and runs are kept.", v.Name), nil
			}
			return fmt.Sprintf("Confirmed %q: %s.", v.Name, scheduleSays(v)), nil
		},
	}
}

func (s *Service) watcherOps() ops {
	return ops{
		create: s.toolCreateWatcher,
		list:   s.toolListWatchers,
		update: func(ctx context.Context, t toolRun, ref Ref, a toolArgs) (string, bool, string, error) {
			p, err := t.watcherPatchOf(ctx, a)
			if err != nil {
				return "", false, "", err
			}
			v, err := s.ProposeWatcherChange(ctx, t.who, t.origin(), ref, p)
			return v.Name, v.Status == Proposed || v.Status == WaitingSecret, watcherPatchSays(p), err
		},
		pause: func(ctx context.Context, t toolRun, ref Ref) (string, error) {
			paused := Paused
			v, err := s.UpdateWatcher(ctx, t.who, ref, WatcherPatch{Status: &paused})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Paused %q. It looks no more until it is resumed.", v.Name), nil
		},
		ask: func(ctx context.Context, t toolRun, ref Ref, action string) (string, bool, error) {
			v, err := s.ProposeWatcherAction(ctx, t.who, t.origin(), ref, action)
			return v.Name, v.Status == Deleted, err
		},
		confirm: func(ctx context.Context, t toolRun, ref Ref) (string, error) {
			v, err := s.Watcher(ctx, t.who, ref)
			if err != nil {
				return "", err
			}
			if v.Status == WaitingSecret {
				return fmt.Sprintf("%q starts once the person makes its secret on the dashboard%s. Never ask for the secret here.",
					v.Name, t.watcherLink(v.ID)), nil
			}
			if v, err = s.ConfirmWatcher(ctx, t.who, t.origin(), Ref{ID: v.ID}); err != nil {
				return "", err
			}
			if v.Status == Deleted {
				return fmt.Sprintf("Deleted %q. Its conversation and runs are kept.", v.Name), nil
			}
			return fmt.Sprintf("Confirmed %q: it %s.", v.Name, watcherSays(v)), nil
		},
	}
}

func (s *Service) toolCreateSchedule(ctx context.Context, t toolRun, a toolArgs) (string, error) {
	zone := deref(a.Timezone)
	w, err := s.whenOf(ctx, t, a, zone)
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", refuse(Invalid, "say when: one of at, every or cron")
	}
	agentID, err := t.agentOf(ctx, a.Agent)
	if err != nil {
		return "", err
	}
	target, err := t.targetOf(a.Target)
	if err != nil {
		return "", err
	}
	v, err := s.CreateSchedule(ctx, t.who, t.origin(), ScheduleInput{
		Name: deref(a.Name), Objective: deref(a.Objective), When: *w, Timezone: zone, AgentID: agentID, Target: target,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Proposed %q: %s. It answers %s. Ask the person to confirm; it starts only after their next message says yes, "+
		"and the proposal lapses in an hour.", v.Name, upcomingSays(v), whereSays(v.Target, v.TargetLabel)), nil
}

func (s *Service) toolListSchedules(ctx context.Context, t toolRun) (string, error) {
	vs, err := s.ListSchedules(ctx, t.who, false, nil, nil, nil, toolListed)
	if err != nil {
		return "", err
	}
	if len(vs) == 0 {
		return "The person has no schedules.", nil
	}
	lines := make([]string, len(vs))
	for i, v := range vs {
		lines[i] = scheduleLine(v)
	}
	return listSays(lines), nil
}

func (s *Service) toolCreateWatcher(ctx context.Context, t toolRun, a toolArgs) (string, error) {
	in := WatcherInput{Name: deref(a.Name), Objective: deref(a.Objective), Source: a.Source, URL: a.URL, Selector: deref(a.Selector)}
	every, err := optDuration(a.Every)
	if err != nil {
		return "", err
	}
	if every != nil {
		in.Every = *every
	}
	if in.Cooldown, err = optDuration(a.Cooldown); err != nil {
		return "", err
	}
	if in.AgentID, err = t.agentOf(ctx, a.Agent); err != nil {
		return "", err
	}
	if in.Target, err = t.targetOf(a.Target); err != nil {
		return "", err
	}
	v, err := s.CreateWatcher(ctx, t.who, t.origin(), in)
	if err != nil {
		return "", err
	}
	if v.Status == WaitingSecret {
		return fmt.Sprintf("Saved %q as waiting for its secret. It does nothing until then. Tell the person what it will do, "+
			"and that they make the secret and copy its URL on the dashboard%s. Never ask them to paste the secret here.",
			v.Name, t.watcherLink(v.ID)), nil
	}
	return fmt.Sprintf("Proposed %q: it %s, and answers %s. Its first look is its baseline and starts nothing. "+
		"Ask the person to confirm; it starts only after their next message says yes, and the proposal lapses in an hour.",
		v.Name, watcherSays(v), whereSays(v.Target, v.TargetLabel)), nil
}

func (s *Service) toolListWatchers(ctx context.Context, t toolRun) (string, error) {
	vs, err := s.ListWatchers(ctx, t.who, false, nil, nil, nil, toolListed)
	if err != nil {
		return "", err
	}
	if len(vs) == 0 {
		return "The person has no watchers.", nil
	}
	lines := make([]string, len(vs))
	for i, v := range vs {
		lines[i] = watcherLine(v)
	}
	return listSays(lines), nil
}
