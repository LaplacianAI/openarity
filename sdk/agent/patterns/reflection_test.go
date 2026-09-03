package patterns

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func reflectSpec(tools ...agent.Tool) agent.Spec {
	s := spec(tools...)
	s.Pattern = agent.PatternReflection
	return s
}

// critiqued is a turn that submits a verdict, which is how the reflecting call
// is meant to answer.
func critiqued(needs bool, note string) turn {
	args, err := json.Marshal(map[string]any{"needs_refinement": needs, "critique": note})
	if err != nil {
		panic(err)
	}
	return answered(calling(CritiqueToolName, string(args)))
}

func TestReflectionAnswersItsOwnName(t *testing.T) {
	t.Parallel()

	if got := Reflection(1).Name(); got != agent.PatternReflection {
		t.Errorf("Name() = %q, want %q", got, agent.PatternReflection)
	}
	if got := ReflectionStreaming(1).Name(); got != agent.PatternReflection {
		t.Errorf("streaming Name() = %q, want %q", got, agent.PatternReflection)
	}
}

// The published algorithm: generate, critique, rewrite. Three calls for one
// cycle when the critique asks for changes.
func TestOneCycleGeneratesThenCritiquesThenRewrites(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("Paris is the capital of Germany.")),
		critiqued(true, "Germany's capital is Berlin."),
		answered(text("Berlin is the capital of Germany.")),
	}}

	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("what is the capital of Germany?"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 3 {
		t.Fatalf("the model was called %d times, want 3", len(model.calls))
	}
	if result.Output != "Berlin is the capital of Germany." {
		t.Errorf("Output = %q, want the rewrite", result.Output)
	}
	if result.Steps != 3 {
		t.Errorf("Steps = %d, want 3", result.Steps)
	}
}

// The whole point of the check phase. An answer the critic is happy with must
// not be rewritten, because a rewrite it did not ask for can only make it
// worse and costs a call either way.
func TestACritiqueThatAsksForNothingStopsTheCycle(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("Berlin.")),
		critiqued(false, ""),
		answered(text("this rewrite should never happen")),
	}}

	s := reflectSpec()
	s.MaxSteps = 7 // three cycles would need it; the point is that two calls happen

	result, err := Reflection(3).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("capital of Germany?"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 2 {
		t.Fatalf("the model was called %d times, want 2 — it rewrote an answer nobody faulted", len(model.calls))
	}
	if result.Output != "Berlin." {
		t.Errorf("Output = %q, want the original answer untouched", result.Output)
	}
	if result.Steps != 2 {
		t.Errorf("Steps = %d, want 2", result.Steps)
	}
}

func TestEveryCycleRunsWhenTheCritiqueKeepsAskingForChanges(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("draft one")),
		critiqued(true, "sharper"),
		answered(text("draft two")),
		critiqued(true, "sharper still"),
		answered(text("draft three")),
	}}

	s := reflectSpec()
	s.MaxSteps = 5

	result, err := Reflection(2).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("write something"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 5 {
		t.Fatalf("the model was called %d times, want 5", len(model.calls))
	}
	if result.Output != "draft three" {
		t.Errorf("Output = %q, want the last rewrite", result.Output)
	}
	if result.Steps != 5 {
		t.Errorf("Steps = %d, want 5", result.Steps)
	}
}

// Judging an answer and improving it are reading tasks. A critic holding tools
// tends to go and redo the work rather than assess what is in front of it.
func TestOnlyTheGeneratingCallCarriesTheSpecsTools(t *testing.T) {
	t.Parallel()

	var ran bool
	count := tool("count", "3", nil, &ran)

	model := &fakeModel{turns: []turn{
		answered(text("about three")),
		critiqued(true, "say the number plainly"),
		answered(text("3")),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(count), Messages: ask("how many?"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := toolNames(model.calls[0].Tools); len(got) != 1 || got[0] != "count" {
		t.Errorf("the generating request carried %v, want the spec's tool", got)
	}

	// The reflecting call gets the critique tool and nothing else — not the
	// spec's tools, and not none, or there would be no way to answer.
	if got := toolNames(model.calls[1].Tools); len(got) != 1 || got[0] != CritiqueToolName {
		t.Errorf("the reflecting request carried %v, want only %s", got, CritiqueToolName)
	}

	if n := len(model.calls[2].Tools); n != 0 {
		t.Errorf("the rewriting request carried %d tools, want none", n)
	}
}

func toolNames(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// Generation is a full ReAct run, so the answer being critiqued can be one the
// model had to go and look up.
func TestTheGeneratingPhaseMayUseTools(t *testing.T) {
	t.Parallel()

	var ran bool
	count := tool("count", "3", nil, &ran)

	model := &fakeModel{turns: []turn{
		answered(calling("count", `{}`)),
		answered(text("there are 3")),
		critiqued(false, ""),
	}}

	s := reflectSpec(count)
	s.MaxSteps = 4

	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("how many?"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !ran {
		t.Error("the tool never ran, so generation could not use it")
	}
	if result.Output != "there are 3" {
		t.Errorf("Output = %q", result.Output)
	}
	if result.Steps != 3 {
		t.Errorf("Steps = %d, want 2 generating plus 1 reflecting", result.Steps)
	}
}

// Reserved before generating, not spent optimistically. Without this a
// tool-using generation consumes the whole budget and the critique never
// happens — which looks like reflection quietly not working.
func TestGenerationCannotSpendTheBudgetTheCyclesNeed(t *testing.T) {
	t.Parallel()

	var ran bool
	count := tool("count", "3", nil, &ran)

	// Enough for one generating call only: 5 total, minus 2 per cycle × 2.
	model := &fakeModel{turns: []turn{
		answered(calling("count", `{}`)),
		answered(text("never reached")),
	}}

	s := reflectSpec(count)
	s.MaxSteps = 5

	_, err := Reflection(2).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("how many?"), Model: model,
	})
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("err = %v, want it to hit the generating ceiling", err)
	}
	if len(model.calls) != 1 {
		t.Errorf("the model was called %d times, want 1 — generation took the cycles' budget", len(model.calls))
	}
}

func TestReflectionRefusesTooFewSteps(t *testing.T) {
	t.Parallel()

	model := &fakeModel{}
	s := reflectSpec()
	s.MaxSteps = 2 // one cycle needs three

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("hello"), Model: model,
	})
	if !errors.Is(err, agent.ErrNoMaxSteps) {
		t.Fatalf("err = %v, want ErrNoMaxSteps", err)
	}
	if len(model.calls) != 0 {
		t.Errorf("the model was called %d times for a run that could not finish", len(model.calls))
	}
	// The number is in the message, because "not enough" without saying how
	// many sends the reader to the source.
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("the error does not say how many steps are needed: %v", err)
	}
}

func TestReflectionRefusesNoCycles(t *testing.T) {
	t.Parallel()

	model := &fakeModel{}
	_, err := Reflection(0).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	})
	if !errors.Is(err, ErrNoCycles) {
		t.Fatalf("err = %v, want ErrNoCycles", err)
	}
	if len(model.calls) != 0 {
		t.Errorf("the model was called %d times", len(model.calls))
	}
}

func TestReflectionRefusesANilModel(t *testing.T) {
	t.Parallel()

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"),
	}); err == nil {
		t.Fatal("Run accepted a nil ModelClient")
	}
}

// A reflecting turn that answers in prose rather than calling the tool leaves
// the pattern with no verdict. Guessing from the text is how "no major changes
// needed, though I would note…" gets read as approval.
func TestAReflectingTurnThatSubmitsNothingIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		answered(text("looks good to me")),
	}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if !errors.Is(err, ErrNoCritique) {
		t.Fatalf("err = %v, want ErrNoCritique", err)
	}
}

func TestACritiqueWithUnreadableArgumentsIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		answered(calling(CritiqueToolName, `{"needs_refinement":`)),
	}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if !errors.Is(err, ErrNoCritique) {
		t.Fatalf("err = %v, want ErrNoCritique", err)
	}
	// Distinguishable from a turn that called nothing at all, or the two
	// failures cannot be told apart in a log.
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("the error does not say the arguments were unreadable: %v", err)
	}
}

// "Change it" with no reason gives the rewriting turn nothing to act on, so it
// rewrites at random. Better to refuse than to spend a call on that.
func TestACritiqueAskingForChangesWithoutSayingWhichIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		critiqued(true, ""),
	}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if !errors.Is(err, ErrNoCritique) {
		t.Fatalf("err = %v, want ErrNoCritique", err)
	}
}

// A verdict of "no change" with an empty critique is the normal happy path and
// must not trip the guard above.
func TestACritiqueAskingForNothingNeedsNoReason(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		critiqued(false, ""),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestATruncatedReflectingTurnIsReported(t *testing.T) {
	t.Parallel()

	cut := answered(text("a partial critique"))
	cut.resp.Finish = agent.FinishLength

	model := &fakeModel{turns: []turn{answered(text("a draft")), cut}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

// The draft was paid for and is still the best answer there is. Returning
// nothing because the rewrite was cut off throws away work the caller owns.
func TestATruncatedRewriteKeepsTheAnswerItAlreadyHad(t *testing.T) {
	t.Parallel()

	cut := answered(text("half a rewri"))
	cut.resp.Finish = agent.FinishLength

	model := &fakeModel{turns: []turn{
		answered(text("the original draft")),
		critiqued(true, "make it better"),
		cut,
	}}

	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if result.Output != "the original draft" {
		t.Errorf("Output = %q, want the draft that survived", result.Output)
	}
	if result.Steps != 3 {
		t.Errorf("Steps = %d, want 3", result.Steps)
	}
}

func TestAFailureWhileGeneratingNamesThePhase(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{{err: errors.New("gateway down")}}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	})
	if err == nil || !strings.Contains(err.Error(), "generating") {
		t.Fatalf("err = %v, want it to name the generating phase", err)
	}
}

func TestAFailureWhileReflectingNamesThePhaseAndTheCycle(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		{err: errors.New("gateway down")},
	}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	})
	if err == nil {
		t.Fatal("Run succeeded against a failing model")
	}
	for _, want := range []string{"reflecting", "cycle 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestAFailureWhileRewritingNamesThePhaseAndTheCycle(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		critiqued(true, "improve it"),
		{err: errors.New("gateway down")},
	}}

	_, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	})
	if err == nil {
		t.Fatal("Run succeeded against a failing model")
	}
	for _, want := range []string{"rewriting", "cycle 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// A caller handing Messages back next turn must see one answer, not the draft,
// the critique and the rewrite — the model would have to guess which stands.
func TestTheRewriteReplacesTheDraftRatherThanFollowingIt(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Messages) != 2 {
		t.Fatalf("Messages has %d entries, want the question and one answer: %v",
			len(result.Messages), texts(result.Messages))
	}
	if got := result.Messages[1].Text(); got != "the rewrite" {
		t.Errorf("the last message is %q, want the rewrite", got)
	}
	for _, m := range result.Messages {
		if strings.Contains(m.Text(), "improve it") {
			t.Error("the critique leaked into the conversation the caller gets back")
		}
	}
}

func texts(msgs []agent.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Text())
	}
	return out
}

// The rewriting turn has to be told what to fix, or it rewrites blind.
func TestTheCritiqueReachesTheRewritingTurn(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "the date is wrong"),
		answered(text("the rewrite")),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found bool
	for _, m := range model.calls[2].Messages {
		if strings.Contains(m.Text(), "the date is wrong") {
			found = true
		}
	}
	if !found {
		t.Errorf("the rewriting request never carried the critique: %v", texts(model.calls[2].Messages))
	}
}

// Replacing the system prompt would drop the agent's identity and every
// guideline the deployment set, and nothing fails visibly when it does.
func TestReflectionAppendsToTheSystemPromptRatherThanReplacingIt(t *testing.T) {
	t.Parallel()

	s := reflectSpec()
	// Spare capacity on purpose: a caller whose slice is exactly full gets a
	// reallocation from append, and a missing clone cannot reproduce.
	s.System = make([]agent.Content, 1, 4)
	s.System[0] = agent.Content{Type: agent.ContentText, Text: "You are a terse assistant.", Cacheable: true}

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("hello"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for i, call := range model.calls {
		if len(call.System) == 0 || call.System[0].Text != "You are a terse assistant." {
			t.Errorf("call %d lost the caller's system prompt: %v", i, call.System)
		}
	}
	if len(model.calls[1].System) != 2 {
		t.Errorf("the reflecting request carried %d system blocks, want the caller's plus the instruction",
			len(model.calls[1].System))
	}

	// Reaching past the length: if the clone is missing, the reflecting
	// instruction was written into the caller's array rather than a copy.
	if got := s.System[:2][1].Text; got != "" {
		t.Errorf("the run wrote %q into the caller's slice", got)
	}
}

// A Spec reused for a second run must not come back carrying the first run's
// tools.
func TestReflectionLeavesTheCallersToolsAlone(t *testing.T) {
	t.Parallel()

	s := reflectSpec()
	s.Tools = make([]agent.Tool, 1, 4)
	s.Tools[0] = tool("count", "3", nil, nil)

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: s, Messages: ask("hello"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(s.Tools) != 1 || s.Tools[0].Name != "count" {
		t.Errorf("the caller's tools are now %v", toolNames(s.Tools))
	}
	if got := s.Tools[:2][1].Name; got != "" {
		t.Errorf("the run wrote a tool named %q into the caller's slice", got)
	}
}

// Every request opens with the conversation the caller was given, not just the
// first — a reflection run inside an existing chat must not forget it.
func TestReflectionCarriesTheConversationIntoEveryPhase(t *testing.T) {
	t.Parallel()

	prior := history()
	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: prior, Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, call := range model.calls {
		assertCarries(t, call, prior)
	}
}

func TestReflectionEmitsAStepForEveryCall(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	events := make(chan agent.Event, 64)
	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model, Events: events,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)

	var steps []int
	var usages int
	for e := range events {
		switch ev := e.(type) {
		case agent.StepEvent:
			steps = append(steps, ev.Step)
		case agent.UsageEvent:
			usages++
		}
	}

	// 1 from generating, then one per phase. Numbered in order, so a reader
	// watching the stream can tell which call is which.
	want := []int{1, 2, 3}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for i, n := range want {
		if steps[i] != n {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
	if usages != 3 {
		t.Errorf("%d usage events, want one per call", usages)
	}
}

func TestReflectionStreamsTheRewriteButNotTheCritique(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("the draft")),
		critiqued(true, "improve it"),
		answered(text("the rewrite")),
	}}

	if _, err := ReflectionStreaming(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The critique is a tool call with no text, so streaming it would show a
	// caller nothing while the run appears to stall.
	if model.streamed[1] {
		t.Error("the reflecting call was streamed")
	}
	if !model.streamed[0] || !model.streamed[2] {
		t.Errorf("streamed = %v, want the generating and rewriting calls streamed", model.streamed)
	}
}

// The critique is read out of the tool call, not dispatched. If a pattern ever
// does dispatch it, that has to be loud rather than an empty string the model
// reads as a tool that did nothing.
func TestTheCritiqueToolRefusesToBeInvoked(t *testing.T) {
	t.Parallel()

	out, err := critiqueTool().Invoke(t.Context(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("the critique tool ran as though it were a real tool")
	}
	if out != "" {
		t.Errorf("it returned %q as well as an error", out)
	}
}
