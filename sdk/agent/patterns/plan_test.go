package patterns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func planSpec(tools ...agent.Tool) agent.Spec {
	s := spec(tools...)
	s.Pattern = agent.PatternPlan
	return s
}

func TestPlanAnswersItsOwnName(t *testing.T) {
	t.Parallel()

	if got := Plan().Name(); got != agent.PatternPlan {
		t.Errorf("Name() = %q, want %q", got, agent.PatternPlan)
	}
	if got := PlanStreaming().Name(); got != agent.PatternPlan {
		t.Errorf("streaming Name() = %q, want %q", got, agent.PatternPlan)
	}
}

// The plan is a turn the model cannot act on. Offering tools would make it a
// first step rather than a plan, and the model would skip straight to acting.
func TestThePlanningRequestCarriesNoTools(t *testing.T) {
	t.Parallel()

	var ran bool
	count := tool("count", "3", nil, &ran)
	model := &fakeModel{turns: []turn{
		answered(text("1. Count them.\n2. Say the number.")),
		answered(calling("count", `{}`)),
		answered(text("3")),
	}}

	if _, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(count), Messages: ask("how many?"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 3 {
		t.Fatalf("the model was called %d times, want 3", len(model.calls))
	}
	if n := len(model.calls[0].Tools); n != 0 {
		t.Errorf("the planning request carried %d tools, want none", n)
	}
	if n := len(model.calls[1].Tools); n != 1 {
		t.Errorf("the acting request carried %d tools, want the spec's one", n)
	}
}

// Replacing the system prompt would drop the agent's identity and every
// guideline the deployment set, and nothing fails visibly when it does.
func TestPlanningAppendsToTheSystemPromptRatherThanReplacingIt(t *testing.T) {
	t.Parallel()

	s := planSpec()
	// Built with spare capacity on purpose. A caller whose slice is full gets
	// a reallocation from append and the missing clone cannot reproduce, which
	// is exactly how this bug survives a test that looks like it covers it.
	s.System = make([]agent.Content, 1, 4)
	s.System[0] = agent.Content{Type: agent.ContentText, Text: "You are a terse assistant.", Cacheable: true}

	model := &fakeModel{turns: []turn{answered(text("1. Answer.")), answered(text("done"))}}
	if _, err := Plan().Run(t.Context(), agent.Input{Spec: s, Messages: ask("hello"), Model: model}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sys := model.calls[0].System
	if len(sys) != 2 {
		t.Fatalf("the planning request carried %d system blocks, want the caller's plus the instruction", len(sys))
	}
	if sys[0].Text != "You are a terse assistant." {
		t.Errorf("the first block is %q, want the caller's prompt", sys[0].Text)
	}
	if !strings.Contains(sys[1].Text, "plan") {
		t.Errorf("the second block is %q, want the planning instruction", sys[1].Text)
	}

	// And the caller's slice was not the one appended to. Reaching past the
	// length they hold is the only way to see it: with a shared array the
	// planning instruction is sitting there, waiting for their next append.
	if len(s.System) != 1 {
		t.Error("the caller's system prompt grew")
	}
	if got := s.System[:2][1].Text; got != "" {
		t.Errorf("the planning instruction was written into the caller's array as %q", got)
	}
}

// The plan must reach the second phase as something the assistant said, or the
// phase that acts has no idea what it agreed to do.
func TestThePlanJoinsTheTranscript(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("1. Say hello.")),
		answered(text("hello")),
	}}

	result, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: ask("greet me"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	acting := model.calls[1].Messages
	if len(acting) != 2 {
		t.Fatalf("the acting request carried %d messages, want the question and the plan", len(acting))
	}
	if acting[1].Role != agent.RoleAssistant || !strings.Contains(acting[1].Text(), "Say hello") {
		t.Errorf("the plan reached the second phase as %+v", acting[1])
	}
	if len(result.Messages) != 3 {
		t.Errorf("the result holds %d messages, want the question, the plan and the answer", len(result.Messages))
	}
}

// MaxSteps is the caller's whole budget. A per-phase allowance lets this pattern
// quietly cost twice what the brain asked for.
func TestThePlanSpendsAStepFromTheCallersBudget(t *testing.T) {
	t.Parallel()

	s := planSpec(tool("count", "3", nil, nil))
	s.MaxSteps = 3

	// A model that never stops calling tools: the run ends by exhausting the
	// ceiling, and where it stops is the measurement.
	model := &fakeModel{turns: []turn{
		answered(text("1. Pattern forever.")),
		answered(calling("count", `{}`)),
		answered(calling("count", `{}`)),
		answered(calling("count", `{}`)),
		answered(calling("count", `{}`)),
	}}

	result, err := Plan().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model})
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if result.Steps != 3 {
		t.Errorf("Steps = %d, want the caller's ceiling of 3", result.Steps)
	}
	if len(model.calls) != 3 {
		t.Errorf("the model was called %d times, want 3 — the plan plus two acting steps", len(model.calls))
	}
}

// A ceiling of one buys a plan and nothing to carry it out.
func TestACeilingOfOneIsRefused(t *testing.T) {
	t.Parallel()

	s := planSpec()
	s.MaxSteps = 1

	model := &fakeModel{turns: []turn{answered(text("1. Answer."))}}
	_, err := Plan().Run(t.Context(), agent.Input{Spec: s, Messages: ask("hello"), Model: model})

	if !errors.Is(err, agent.ErrNoMaxSteps) {
		t.Fatalf("err = %v, want it to wrap ErrNoMaxSteps", err)
	}
	if len(model.calls) != 0 {
		t.Error("a refused run still called the model")
	}
}

func TestPlanWithNoModelClientIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := Plan().Run(t.Context(), agent.Input{Spec: planSpec()}); err == nil {
		t.Error("a run with no ModelClient was accepted")
	}
}

// A plan cut off at MaxTokens is half a plan, and the phase that follows would
// work from it without knowing.
func TestATruncatedPlanStopsTheRun(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{{resp: agent.Response{
		Message: text("1. First I will"),
		Finish:  agent.FinishLength,
		Usage:   agent.Usage{InputTokens: 10, OutputTokens: 5},
	}}}}

	result, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(model.calls) != 1 {
		t.Error("the run kept going after a truncated plan")
	}
	// What happened is still reported. The tokens are the Runner's to fill in —
	// it does so on the error path too — so what this pattern owes is the step
	// count a caller retrying with a bigger ceiling needs.
	if result.Steps != 1 {
		t.Errorf("Steps = %d, want the plan's one step", result.Steps)
	}
}

// The planning request carries no tools, so a turn with tool calls means the
// gateway ignored the field. Appending it leaves a tool call nothing answers,
// and the next request is rejected for it.
func TestAPlanningTurnThatCalledAToolIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(calling("count", `{}`))}}

	_, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, ErrPlanCalledATool) {
		t.Fatalf("err = %v, want ErrPlanCalledATool", err)
	}
	if len(model.calls) != 1 {
		t.Error("the run continued past a planning turn it should have refused")
	}
}

// Both patterns count their own steps from one. Unrenumbered the transcript reads
// 1, 1, 2 and looks like a retry rather than progress.
func TestStepsAreNumberedAcrossBothPhases(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("1. Count.\n2. Report.")),
		answered(calling("count", `{}`)),
		answered(text("3")),
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var steps []int
	go func() {
		defer close(done)
		for e := range events {
			if s, ok := e.(agent.StepEvent); ok {
				steps = append(steps, s.Step)
			}
		}
	}()

	if _, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(tool("count", "3", nil, nil)), Messages: ask("how many?"),
		Model: model, Events: events,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)
	<-done

	if len(steps) != 3 {
		t.Fatalf("steps = %v, want three", steps)
	}
	for i, want := range []int{1, 2, 3} {
		if steps[i] != want {
			t.Errorf("steps = %v, want [1 2 3]", steps)
			break
		}
	}
}

// The plan's tokens are this pattern's to report. ReAct counted only what it did.
func TestThePlansTokensAreCounted(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		{resp: agent.Response{
			Message: text("1. Answer."), Finish: agent.FinishStop,
			Usage: agent.Usage{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2},
		}},
		{resp: agent.Response{
			Message: text("done"), Finish: agent.FinishStop,
			Usage: agent.Usage{InputTokens: 20, OutputTokens: 6, CachedInputTokens: 3},
		}},
	}}

	// Through the Runner, because the count is taken at the model client. The
	// planning call is the one a naive total would miss: it happens before the
	// pattern this one delegates to has started.
	result := through(t, Plan(), planSpec(), ask("hello"), model)

	want := agent.Usage{InputTokens: 30, OutputTokens: 10, CachedInputTokens: 5}
	if result.Usage != want {
		t.Errorf("Usage = %+v, want %+v", result.Usage, want)
	}
	if result.Steps != 2 {
		t.Errorf("Steps = %d, want 2", result.Steps)
	}
}

// A batch run passes no channel, and that must not be a special case anywhere
// in the renumbering.
func TestPlanWithNilEventsIsNotAnError(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(text("1. Answer.")), answered(text("done"))}}
	result, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: ask("hello"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q", result.Output)
	}
}

// Streaming applies to both phases: the plan appears as it is written, which is
// the only reason to choose the streaming constructor.
func TestPlanStreamingStreamsThePlanToo(t *testing.T) {
	t.Parallel()

	model := &fakeModel{
		deltas: []string{"1. ", "Answer."},
		turns:  []turn{answered(text("1. Answer.")), answered(text("done"))},
	}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var deltas []string
	go func() {
		defer close(done)
		for e := range events {
			if txt, ok := e.(agent.TextEvent); ok {
				deltas = append(deltas, txt.Delta)
			}
		}
	}()

	result, err := PlanStreaming().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: ask("hello"), Model: model, Events: events,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)
	<-done

	if len(deltas) < 2 || deltas[0] != "1. " {
		t.Errorf("deltas = %v, want the plan's fragments first", deltas)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q", result.Output)
	}
}

// A model that fails on the planning call must say which phase failed. "step 1"
// from ReAct would point at the wrong one.
func TestAFailedPlanningCallNamesThePhase(t *testing.T) {
	t.Parallel()

	boom := errors.New("the gateway refused")
	model := &fakeModel{turns: []turn{{err: boom}}}

	_, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: ask("hello"), Model: model,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the gateway's error", err)
	}
	if !strings.Contains(err.Error(), "planning") {
		t.Errorf("err = %v, want it to name the phase", err)
	}
}

// Cancelling mid-run must not leave the renumbering goroutine holding a channel
// nobody reads. Under -race and with a consumer that stopped, this hangs
// without the ctx.Done arm.
func TestPlanSurvivesAConsumerThatStoppedReading(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	model := &fakeModel{turns: []turn{
		answered(text("1. Answer.")),
		answered(text("done")),
	}}

	// Unbuffered and nobody reading: every Emit past the first blocks.
	events := make(chan agent.Event)
	go func() {
		<-events
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Plan().Run(ctx, agent.Input{
			Spec: planSpec(), Messages: ask("hello"), Model: model, Events: events,
		})
	}()

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("the run wedged on a consumer that stopped reading")
	}
}

// The forwarding goroutine makes the same bargain agent.Input.Emit makes. Sent
// straight at it with a cancelled context and a channel nobody reads, it has to
// give up rather than hold the event forever.
func TestRenumberGivesUpWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Unbuffered and nobody reading: delivery is impossible.
	in, drain := renumber(ctx, make(chan agent.Event), 1)
	in <- agent.StepEvent{Step: 1}

	done := make(chan struct{})
	go func() { defer close(done); drain() }()

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("the forwarder held an event nobody was reading")
	}
}

// A nil channel is the normal case for a batch run and must not need a guard at
// the call site.
func TestRenumberOnANilChannelIsANoOp(t *testing.T) {
	t.Parallel()

	in, drain := renumber(t.Context(), nil, 1)
	if in != nil {
		t.Error("a nil output produced a channel to forward into")
	}
	drain()
}

// Both phases carry the conversation. The planning call needs it to know what
// is being asked; the acting call needs it for the same reason plus the plan.
func TestPlanCarriesTheConversationIntoBothPhases(t *testing.T) {
	t.Parallel()

	prior := history()
	model := &fakeModel{turns: []turn{
		answered(text("1. Count the brain's issues.")),
		answered(text("3")),
	}}

	if _, err := Plan().Run(t.Context(), agent.Input{
		Spec: planSpec(), Messages: prior, Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 2 {
		t.Fatalf("the model was called %d times, want 2", len(model.calls))
	}
	for i, call := range model.calls {
		t.Run(fmt.Sprintf("call %d", i), func(t *testing.T) {
			assertCarries(t, call, prior)
		})
	}
}
