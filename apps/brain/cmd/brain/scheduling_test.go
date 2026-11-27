package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/runtime"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers"
)

// The runtime names the tools it was not given, and lets plan come before a
// confirm, by names the rules own: they must be the same.
func TestTheRuntimeNamesTheScheduleToolsAsTheRulesDo(t *testing.T) {
	s := scheduling{service: triggers.NewService(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	var names []string
	for _, tl := range s.StandIns() {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, runtime.ScheduleToolNames) {
		t.Errorf("the rules' tools are %v, the runtime names %v", names, runtime.ScheduleToolNames)
	}
	if !slices.Equal(names, []string{triggers.ScheduleToolName, triggers.WatchToolName}) {
		t.Errorf("stand-ins %v", names)
	}
	if runtime.PlanToolName != triggers.PlanToolName {
		t.Errorf("the runtime's plan tool is %q; the rules let %q come before a confirm", runtime.PlanToolName, triggers.PlanToolName)
	}
	// A tool the runtime does not mark but the rules do not know reads
	// nothing would let a read come before a confirm.
	if !slices.Equal(slices.Sorted(slices.Values(runtime.ReadsNothing)), slices.Sorted(slices.Values(triggers.ReadsNothing))) {
		t.Errorf("the runtime leaves %v unmarked, the rules %v", runtime.ReadsNothing, triggers.ReadsNothing)
	}
}

// An agent the run may not name reaches the model as a refusal; a failure to
// look it up does not.
func TestAnAgentTheRunMayNotNameIsARefusal(t *testing.T) {
	var refused *triggers.Error
	if err := agentRefusal(fmt.Errorf("wrapped: %w", &runtime.NotSchedulable{Msg: "no agent in this team is named \"x\""})); !errors.As(err, &refused) ||
		refused.Kind != triggers.Invalid || refused.Msg != `no agent in this team is named "x"` {
		t.Errorf("a name the run may not use = %v", err)
	}
	down := errors.New("the database is down")
	if err := agentRefusal(down); !errors.Is(err, down) || triggers.KindOf(err) != 0 {
		t.Errorf("a failure = %v, want it kept as a failure", err)
	}
	if agentRefusal(nil) != nil {
		t.Error("no error became one")
	}
}
