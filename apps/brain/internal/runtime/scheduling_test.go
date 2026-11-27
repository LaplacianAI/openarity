package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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
