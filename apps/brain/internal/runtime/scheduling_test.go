package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/sdk/agent"
)

// fakeScheduling hands out the two tools, or why, recording what it was asked.
type fakeScheduling struct {
	why   string
	asked []SchedulingRun
}

func (f *fakeScheduling) Tools(_ context.Context, r SchedulingRun) ([]agent.Tool, string, error) {
	f.asked = append(f.asked, r)
	if f.why != "" {
		return nil, f.why, nil
	}
	return f.StandIns(), "", nil
}

func (*fakeScheduling) StandIns() []agent.Tool {
	out := make([]agent.Tool, len(ScheduleToolNames))
	for i, n := range ScheduleToolNames {
		out[i] = agent.Tool{
			Name: n, Schema: json.RawMessage(`{"type":"object"}`),
			Invoke: func(context.Context, json.RawMessage) (string, error) { return "made", nil },
		}
	}
	return out
}

func newSchedulingFixture() (*fixture, *fakeScheduling) {
	f := newFixture()
	f.teamModel()
	user := uuid.New()
	f.run.UserID = &user
	s := &fakeScheduling{}
	f.builder.WithScheduling(s)
	return f, s
}

func scheduleToolsIn(built Built) []string {
	var names []string
	for _, t := range built.Spec.Tools {
		if slices.Contains(ScheduleToolNames, t.Name) {
			names = append(names, t.Name)
		}
	}
	return names
}

// The hub holds the tools and its schedules run the hub; an agent holds them
// only by its own grant, and its schedules run it.
func TestTheHubAndAnAgentGrantedItHoldTheScheduleTools(t *testing.T) {
	f, s := newSchedulingFixture()
	f.asOrchestrator()
	if got := scheduleToolsIn(f.build(t)); !slices.Equal(got, ScheduleToolNames) {
		t.Fatalf("the hub holds %v, want %v", got, ScheduleToolNames)
	}
	if r := s.asked[0]; r.Self != nil || r.User != *f.run.UserID || r.Session != f.run.SessionID || r.Run != f.run.ID {
		t.Errorf("asked for %+v: the hub's schedule runs the hub, as the run's person", r)
	}

	f, s = newSchedulingFixture()
	if got := scheduleToolsIn(f.build(t)); len(got) != 0 || len(s.asked) != 0 {
		t.Errorf("an agent without the grant holds %v (asked %d times)", got, len(s.asked))
	}
	f.agent.Schedule = true
	f.store.agents[f.agent.ID] = f.agent
	if got := scheduleToolsIn(f.build(t)); !slices.Equal(got, ScheduleToolNames) {
		t.Fatalf("an agent granted it holds %v", got)
	}
	if r := s.asked[0]; r.Self == nil || *r.Self != f.agent.ID {
		t.Errorf("an agent's schedule runs %v, want itself", r.Self)
	}
}

// A delegated run, the builder's, and a run no person is there for hold
// neither tool, and the rules are not asked.
func TestOnlyATopLevelRunForAPersonHoldsTheScheduleTools(t *testing.T) {
	for name, set := range map[string]func(*fixture){
		"delegated": func(f *fixture) { parent := uuid.New(); f.run.ParentRunID = &parent },
		"no person": func(f *fixture) { f.run.UserID = nil },
	} {
		f, s := newSchedulingFixture()
		f.asOrchestrator()
		set(f)
		if got := scheduleToolsIn(f.build(t)); len(got) != 0 || len(s.asked) != 0 {
			t.Errorf("%s: holds %v (asked %d times)", name, got, len(s.asked))
		}
	}
	f, s := newSchedulingFixture()
	f.asBuilder()
	if got := scheduleToolsIn(f.build(t)); len(got) != 0 || len(s.asked) != 0 {
		t.Errorf("the builder holds %v (asked %d times)", got, len(s.asked))
	}
}

// A run the rules refuse is told why, for each tool.
func TestARunThatMayNotScheduleSaysWhy(t *testing.T) {
	f, s := newSchedulingFixture()
	f.asOrchestrator()
	s.why = "the team's settings do not let agents schedule"
	built := f.build(t)
	if got := scheduleToolsIn(built); len(got) != 0 {
		t.Fatalf("holds %v", got)
	}
	var said []string
	for _, u := range built.Unavailable {
		if u.Reason == s.why {
			said = append(said, u.Name)
		}
	}
	if !slices.Equal(said, ScheduleToolNames) {
		t.Errorf("unavailable %+v, want both tools saying why", built.Unavailable)
	}
}

// A replay holds the tools as stand-ins: named as the run's, never made.
func TestAReplayHoldsTheScheduleToolsAsStandIns(t *testing.T) {
	f, s := newSchedulingFixture()
	f.asOrchestrator()
	f.builder.replay = true
	built := f.build(t)
	if got := scheduleToolsIn(built); !slices.Equal(got, ScheduleToolNames) || len(s.asked) != 0 {
		t.Fatalf("a replay holds %v (asked %d times)", got, len(s.asked))
	}
	for _, tl := range built.Spec.Tools {
		if !slices.Contains(ScheduleToolNames, tl.Name) {
			continue
		}
		r, _, err := tl.Call(t.Context(), json.RawMessage(`{}`))
		if r.Output == "made" || (err == nil && !strings.Contains(r.Output, "Not made")) {
			t.Errorf("%s in a replay answered %q, %v: want it not made", tl.Name, r.Output, err)
		}
	}
}

// A schedule runs the agent talking, or one it may hand work to; another
// agent is refused.
func TestAScheduleNamesOnlyAnAgentTheRunMayHandWorkTo(t *testing.T) {
	f, _ := newSchedulingFixture()
	if id, err := f.builder.schedulable(t.Context(), f.agent, f.run, strings.ToUpper(f.agent.Name)); err != nil || id != f.agent.ID {
		t.Errorf("naming itself where it hands no work = %s, %v", id, err)
	}
	ds, _ := f.withTeam(1)
	other := db.Agent{ID: uuid.New(), TeamID: f.run.TeamID, Name: "billing"}
	ds.agents = append(ds.agents, other)

	f.agent.Delegation = "none"
	if _, err := f.builder.schedulable(t.Context(), f.agent, f.run, "billing"); err == nil {
		t.Error("an agent that hands no work on named another")
	}
	if id, err := f.builder.schedulable(t.Context(), f.agent, f.run, f.agent.Name); err != nil || id != f.agent.ID {
		t.Errorf("naming itself = %s, %v", id, err)
	}
	f.agent.Delegation = "listed"
	ds.delegates[f.agent.ID] = []uuid.UUID{other.ID}
	if id, err := f.builder.schedulable(t.Context(), f.agent, f.run, "billing"); err != nil || id != other.ID {
		t.Errorf("a listed delegate = %s, %v", id, err)
	}
	if _, err := f.builder.schedulable(t.Context(), f.agent, f.run, "nobody"); err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Errorf("an agent not in the team: %v", err)
	}
}

// Calling any tool but the two and plan marks the run as having read, found
// tools too; the schedule tools are told so.
func TestACallMarksTheRunAsHavingRead(t *testing.T) {
	tool := func(name string) agent.Tool {
		return agent.Tool{Name: name, Invoke: func(context.Context, json.RawMessage) (string, error) { return "", nil }}
	}
	found := agent.Tool{Name: "github__search", Result: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}}
	built := Built{outside: &atomic.Bool{}}
	built.Spec.Tools = []agent.Tool{tool("schedule"), tool("watch"), tool(PlanToolName), tool("web_fetch")}
	built.Spec.Discovery = &agent.Discovery{Tools: []agent.Tool{found}}
	built.noteReads()

	call := func(tl agent.Tool) {
		t.Helper()
		if _, _, err := tl.Call(t.Context(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tl := range built.Spec.Tools[:3] {
		call(tl)
	}
	if built.outside.Load() {
		t.Fatal("the schedule tools or plan marked the run as having read")
	}
	call(built.Spec.Tools[3])
	if !built.outside.Load() {
		t.Error("web_fetch did not mark the run")
	}
	built.outside.Store(false)
	call(built.Spec.Discovery.Tools[0])
	if !built.outside.Load() {
		t.Error("a found tool did not mark the run")
	}
}

// What the schedule tools are told is the flag a build of the run sets.
func TestTheScheduleToolsReadTheRunsFlag(t *testing.T) {
	f, s := newSchedulingFixture()
	f.asOrchestrator()
	built := f.build(t)
	read := s.asked[0].ReadOutside
	if read() {
		t.Fatal("a new run has read")
	}
	for _, tl := range built.Spec.Tools {
		if tl.Name == GraphToolName {
			_, _, _ = tl.Call(t.Context(), json.RawMessage(`{"query":"x"}`))
		}
	}
	if !read() {
		t.Error("a call to graph is not seen by the schedule tools")
	}

	// A list, which the tools mark themselves, is seen too.
	f.build(t)
	r := s.asked[len(s.asked)-1]
	if r.ReadOutside() {
		t.Fatal("a new run has read")
	}
	r.NoteRead()
	if !r.ReadOutside() {
		t.Error("a list the tools noted is not seen by them")
	}
}
