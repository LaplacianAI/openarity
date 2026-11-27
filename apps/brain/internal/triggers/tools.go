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
// delete, a resume, a run now) waits for the same yes (changes.go). The yes
// is the person's reply carrying the code the tool's answer gave, and a
// confirm comes before the run reads anything someone else wrote, so text a
// run read can be neither. No webhook secret ever passes through them.
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
	// PlanToolName is the runtime's plan tool, which reads nothing.
	PlanToolName = "plan"
)

// ReadsNothing are the tools whose call does not mark a run as having read:
// the runtime marks every other. The two tools' list reads, and marks it
// itself (ToolRun.NoteRead).
var ReadsNothing = []string{ScheduleToolName, WatchToolName, PlanToolName}

// quietActions are the actions of the two tools that read nothing.
var quietActions = []string{"create", "update", "pause", askResume, askDelete, askRunNow, "confirm"}

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
	// ReadOutside is whether the run has read anything since it was built:
	// called a tool not in ReadsNothing, or a list. The calls it made before
	// a wait are read from what it recorded.
	ReadOutside func() bool
	// NoteRead marks the run as having read, as a list does: it answers with
	// what earlier runs wrote.
	NoteRead func()
	// Dashboard is the origin the dashboard is served from, for a link.
	Dashboard string
}

// heldRun is a run the tools were built for, with what its checks found.
type heldRun struct {
	ToolRun
	who Who
	// chat is the run's conversation as a place to answer in, or zero on
	// the dashboard, where here is a conversation of its own.
	chat Target
	// channel is the channel the run's conversation is on, for "me".
	channel   uuid.UUID
	dashboard bool
}

func (t heldRun) origin() Origin { return Origin{Run: t.Run, Session: t.Session} }

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
	var t heldRun
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
func probeRun(ctx context.Context, q *db.Queries, r ToolRun) (heldRun, string, error) {
	t := heldRun{ToolRun: r, who: Who{Team: r.Team, User: r.User}, dashboard: true}
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
	create func(ctx context.Context, t heldRun, a toolArgs) (string, error)
	list   func(ctx context.Context, t heldRun) (string, error)
	// update holds a change, or revises a proposal.
	update  func(ctx context.Context, t heldRun, ref Ref, a toolArgs) (held, error)
	pause   func(ctx context.Context, t heldRun, ref Ref) (string, error)
	ask     func(ctx context.Context, t heldRun, ref Ref, action string) (held, error)
	confirm func(ctx context.Context, t heldRun, ref Ref) (string, error)
}

// held is what an update or an ask left waiting for the person's yes.
type held struct {
	name string
	// code is the yes code, none when nothing waits.
	code string
	// revised is a proposal revised in place; deleted, one deleted at once.
	revised, deleted bool
	// change is what an update sets, said for a person.
	change string
}

func heldOf(name string, code *string, revised, deleted bool, change string) held {
	return held{name: name, code: deref(code), revised: revised, deleted: deleted, change: change}
}

func (s *Service) scheduleTool(t heldRun) agent.Tool {
	return s.tool(t, ScheduleToolName, scheduleToolDescription, scheduleSchema, s.scheduleOps())
}

func (s *Service) watchTool(t heldRun) agent.Tool {
	return s.tool(t, WatchToolName, watchToolDescription, watchSchema, s.watcherOps())
}

func (s *Service) tool(t heldRun, name, description, schema string, o ops) agent.Tool {
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
func (s *Service) dispatch(ctx context.Context, t heldRun, o ops, a toolArgs) (string, error) {
	switch a.Action {
	case "create":
		return o.create(ctx, t, a)
	case "list":
		if t.NoteRead != nil {
			t.NoteRead()
		}
		return o.list(ctx, t)
	}
	ref, err := a.ref()
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "update":
		h, err := o.update(ctx, t, ref, a)
		if err != nil {
			return "", err
		}
		if h.revised {
			return revisedSays(h.name, h.code), nil
		}
		return askedSays(h.name, "to change "+h.change, h.code), nil
	case "pause":
		return o.pause(ctx, t, ref)
	case askDelete, askResume, askRunNow:
		h, err := o.ask(ctx, t, ref, a.Action)
		if err != nil {
			return "", err
		}
		if h.deleted {
			return fmt.Sprintf("Deleted the proposal %q; it never ran.", h.name), nil
		}
		return askedSays(h.name, ask{Action: a.Action}.says(), h.code), nil
	case "confirm":
		if err := s.confirmable(ctx, t); err != nil {
			return "", err
		}
		return o.confirm(ctx, t, ref)
	}
	return "", refuse(Invalid, "action is create, list, update, pause, resume, delete, run_now or confirm")
}

// confirmable refuses a confirm in a run that has read what someone else
// wrote: a page, a file, a server's answer, another agent's or a list of what
// earlier runs wrote could be the text that says to confirm (*user*,
// 2026-11-27).
func (s *Service) confirmable(ctx context.Context, t heldRun) error {
	read := t.ReadOutside != nil && t.ReadOutside()
	if !read {
		var err error
		if read, err = s.store.RunCalledOtherThan(ctx, db.RunCalledOtherThanParams{
			RunID: t.Run, Quiet: []string{PlanToolName}, Acts: []string{ScheduleToolName, WatchToolName}, Actions: quietActions,
		}); err != nil {
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
		update: func(ctx context.Context, t heldRun, ref Ref, a toolArgs) (held, error) {
			p, err := s.schedulePatchOf(ctx, t, ref, a)
			if err != nil {
				return held{}, err
			}
			v, err := s.ProposeScheduleChange(ctx, t.who, t.origin(), ref, p)
			return heldOf(v.Name, v.YesCode, v.Status == Proposed, false, patchSays(p)), err
		},
		pause: func(ctx context.Context, t heldRun, ref Ref) (string, error) {
			v, err := s.Pause(ctx, t.who, ref)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Paused %q. It runs no more until it is resumed.", v.Name), nil
		},
		ask: func(ctx context.Context, t heldRun, ref Ref, action string) (held, error) {
			v, err := s.ProposeScheduleAction(ctx, t.who, t.origin(), ref, action)
			return heldOf(v.Name, v.YesCode, false, v.Status == Deleted, ""), err
		},
		confirm: func(ctx context.Context, t heldRun, ref Ref) (string, error) {
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
		update: func(ctx context.Context, t heldRun, ref Ref, a toolArgs) (held, error) {
			p, err := t.watcherPatchOf(ctx, a)
			if err != nil {
				return held{}, err
			}
			v, err := s.ProposeWatcherChange(ctx, t.who, t.origin(), ref, p)
			return heldOf(v.Name, v.YesCode, v.Status == Proposed || v.Status == WaitingSecret, false, watcherPatchSays(p)), err
		},
		pause: func(ctx context.Context, t heldRun, ref Ref) (string, error) {
			v, err := s.PauseWatcher(ctx, t.who, ref)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Paused %q. It looks no more until it is resumed.", v.Name), nil
		},
		ask: func(ctx context.Context, t heldRun, ref Ref, action string) (held, error) {
			v, err := s.ProposeWatcherAction(ctx, t.who, t.origin(), ref, action)
			return heldOf(v.Name, v.YesCode, false, v.Status == Deleted, ""), err
		},
		confirm: func(ctx context.Context, t heldRun, ref Ref) (string, error) {
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

func (s *Service) toolCreateSchedule(ctx context.Context, t heldRun, a toolArgs) (string, error) {
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
	return fmt.Sprintf("Proposed %q: %s. It answers %s. %s", v.Name, upcomingSays(v), whereSays(v.Target, v.TargetLabel),
		replyWith(v.YesCode, "start it")), nil
}

func (s *Service) toolListSchedules(ctx context.Context, t heldRun) (string, error) {
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

func (s *Service) toolCreateWatcher(ctx context.Context, t heldRun, a toolArgs) (string, error) {
	in := WatcherInput{Name: deref(a.Name), Objective: deref(a.Objective), Source: a.Source, URL: a.URL, Selector: deref(a.Selector)}
	var err error
	switch {
	case a.Every != "":
		if in.Every, err = durationOf(a.Every); err != nil {
			return "", err
		}
	case a.Source != SourceWebhook:
		// A page or feed looks as often as the team allows, unless told.
		l, err := s.limits(ctx, t.Team)
		if err != nil {
			return "", err
		}
		in.Every = l.MinInterval
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
	return fmt.Sprintf("Proposed %q: it %s, and answers %s. Its first look is its baseline and starts nothing. %s",
		v.Name, watcherSays(v), whereSays(v.Target, v.TargetLabel), replyWith(v.YesCode, "start it")), nil
}

func (s *Service) toolListWatchers(ctx context.Context, t heldRun) (string, error) {
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
