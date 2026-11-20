package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

// reflectKept runs one cycle against model and returns what came back and the
// skips it reported: a review or rewrite that fails keeps the answer it was
// working on rather than failing the run.
func reflectKept(t *testing.T, model *fakeModel) (agent.Result, []agent.SkippedEvent) {
	t.Helper()
	events := make(chan agent.Event, 64)
	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model, Events: events,
	})
	close(events)
	if err != nil {
		t.Fatalf("Run: %v, want the answer kept", err)
	}
	var skipped []agent.SkippedEvent
	for e := range events {
		if s, ok := e.(agent.SkippedEvent); ok {
			skipped = append(skipped, s)
		}
	}
	return result, skipped
}

// skippedFor checks one skip of what, at step, whose reason says each of want.
func skippedFor(t *testing.T, got []agent.SkippedEvent, what string, step int, want ...string) {
	t.Helper()
	if len(got) != 1 || got[0].What != what || got[0].Step != step {
		t.Fatalf("skipped %+v, want one %q at step %d", got, what, step)
	}
	for _, w := range want {
		if !strings.Contains(got[0].Reason, w) {
			t.Errorf("the reason %q does not say %q", got[0].Reason, w)
		}
	}
}

// A reflecting turn that answers in prose rather than calling the tool leaves
// the pattern with no verdict. Guessing from the text is how "no major changes
// needed, though I would note…" gets read as approval; failing the run is how
// a finished answer reaches nobody. So it is asked once more, and if that
// submits nothing either, the draft stands and the skip says why.
func TestAReflectingTurnThatSubmitsNothingTwiceKeepsTheDraft(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		answered(text("looks good to me")),
		answered(text("as I said, it looks good")),
	}}
	result, skipped := reflectKept(t, model)
	if result.Output != "a draft" || result.Steps != 2 {
		t.Errorf("Output %q in %d steps, want the draft in 2", result.Output, result.Steps)
	}
	if len(model.calls) != 3 {
		t.Errorf("the model was called %d times, want the draft and the review asked twice", len(model.calls))
	}
	skippedFor(t, skipped, "critique", 2, ErrNoCritique.Error(), "asked twice", "cycle 1")
}

// A model that thinks will not be told which tool to call, so the client asks
// without saying and the review often comes back written out. Asked again,
// with that review before it, the model submits it, and the verdict is used:
// staging's team-lead lost its review this way in 11 runs of 23.
func TestAReviewWrittenOutIsAskedForAgainAndUsed(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("Paris is the capital of Germany.")),
		answered(text("That is wrong: Germany's capital is Berlin.")),
		critiqued(true, "Germany's capital is Berlin."),
		answered(text("Berlin is the capital of Germany.")),
	}}
	events := make(chan agent.Event, 64)
	result, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("what is the capital of Germany?"), Model: model, Events: events,
	})
	close(events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "Berlin is the capital of Germany." || result.Steps != 3 {
		t.Errorf("Output %q in %d steps, want the rewrite in 3", result.Output, result.Steps)
	}

	if len(model.calls) != 4 {
		t.Fatalf("the model was called %d times, want 4", len(model.calls))
	}
	again := model.calls[2]
	if again.MustCall != CritiqueToolName {
		t.Errorf("the second ask must call %q, want %q", again.MustCall, CritiqueToolName)
	}
	// The first ask's conversation, then one user turn holding the review
	// and the ask: the draft stays the last assistant turn, and the reply is
	// never replayed as one.
	first := model.calls[1].Messages
	if len(again.Messages) != len(first)+1 {
		t.Fatalf("the second ask has %d messages, want the first's %d and one more", len(again.Messages), len(first))
	}
	if last := first[len(first)-1]; last.Role != agent.RoleAssistant || last.Text() != "Paris is the capital of Germany." {
		t.Errorf("the first ask ends %+v, want the draft", last)
	}
	added := again.Messages[len(first)]
	if added.Role != agent.RoleUser || !strings.Contains(added.Text(), "<review>\nThat is wrong: Germany's capital is Berlin.\n</review>") ||
		!strings.Contains(added.Text(), CritiqueToolName) {
		t.Errorf("the added turn is %+v, want a user turn holding the fenced review and naming %s", added, CritiqueToolName)
	}
	// The written review is the critic's, never the person's: the rewrite
	// reads the draft and the submitted critique, not the prose.
	for _, m := range model.calls[3].Messages {
		if m.Text() == "That is wrong: Germany's capital is Berlin." {
			t.Error("the rewrite was shown the review written out")
		}
	}

	var usages, skips int
	for e := range events {
		switch e.(type) {
		case agent.UsageEvent:
			usages++
		case agent.SkippedEvent:
			skips++
		}
	}
	if usages != 4 || skips != 0 {
		t.Errorf("%d usage events and %d skips, want one usage per call and no skip", usages, skips)
	}
}

// The critique is asked for by name, so a model that can be told to call it is.
func TestTheReflectingTurnMustCallTheCritique(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(text("a draft")), critiqued(false, "")}}
	if _, err := Reflection(1).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("write something"), Model: model,
	}); err != nil {
		t.Fatal(err)
	}
	reqs := model.calls
	if len(reqs) != 2 || reqs[0].MustCall != "" || reqs[1].MustCall != CritiqueToolName {
		t.Errorf("must call %q then %q, want nothing then %q", reqs[0].MustCall, reqs[1].MustCall, CritiqueToolName)
	}
}

func TestACritiqueWithUnreadableArgumentsKeepsTheDraft(t *testing.T) {
	t.Parallel()

	result, skipped := reflectKept(t, &fakeModel{turns: []turn{
		answered(text("a draft")),
		answered(calling(CritiqueToolName, `{"needs_refinement":`)),
	}})
	if result.Output != "a draft" {
		t.Errorf("Output = %q, want the draft", result.Output)
	}
	// Distinguishable from a turn that called nothing at all, or the two
	// failures cannot be told apart in a trace.
	skippedFor(t, skipped, "critique", 2, "JSON")
}

// "Change it" with no reason gives the rewriting turn nothing to act on, so it
// would rewrite at random. Better to keep the draft than spend a call on that.
func TestACritiqueAskingForChangesWithoutSayingWhichKeepsTheDraft(t *testing.T) {
	t.Parallel()

	result, skipped := reflectKept(t, &fakeModel{turns: []turn{
		answered(text("a draft")),
		critiqued(true, ""),
	}})
	if result.Output != "a draft" {
		t.Errorf("Output = %q, want the draft", result.Output)
	}
	skippedFor(t, skipped, "critique", 2, "without saying which")
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

func TestATruncatedReflectingTurnKeepsTheDraft(t *testing.T) {
	t.Parallel()

	cut := answered(text("a partial critique"))
	cut.resp.Finish = agent.FinishLength

	result, skipped := reflectKept(t, &fakeModel{turns: []turn{answered(text("a draft")), cut}})
	if result.Output != "a draft" {
		t.Errorf("Output = %q, want the draft", result.Output)
	}
	skippedFor(t, skipped, "critique", 2, agent.ErrTruncated.Error())
}

// The draft was paid for and is still the best answer there is. Returning
// nothing because the rewrite was cut off throws away work the caller owns.
func TestARewriteCutOffEveryTimeKeepsTheAnswerItAlreadyHad(t *testing.T) {
	t.Parallel()

	result, skipped := reflectKept(t, &fakeModel{turns: append([]turn{
		answered(text("the original draft")),
		critiqued(true, "make it better"),
	}, cutTurns("half a rewri", 1+continuations)...)})
	skippedFor(t, skipped, "rewrite", 3, agent.ErrTruncated.Error())
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

// A model that fails while reviewing has still written the draft: it stands,
// and the skip names the phase, the cycle and the failure.
func TestAFailureWhileReflectingKeepsTheDraftNamingThePhaseAndTheCycle(t *testing.T) {
	t.Parallel()

	result, skipped := reflectKept(t, &fakeModel{turns: []turn{
		answered(text("a draft")),
		{err: errors.New("gateway down")},
	}})
	if result.Output != "a draft" {
		t.Errorf("Output = %q, want the draft", result.Output)
	}
	skippedFor(t, skipped, "critique", 2, "reflecting", "cycle 1", "gateway down")
}

// A cancelled run stops: nobody is waiting for the draft.
func TestACancelledReviewFailsTheRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	model := &fakeModel{turns: []turn{
		answered(text("a draft")),
		{err: context.Canceled, before: cancel},
	}}
	_, err := Reflection(1).Run(ctx, agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}

func TestAFailureWhileRewritingKeepsTheDraftNamingThePhaseAndTheCycle(t *testing.T) {
	t.Parallel()

	result, skipped := reflectKept(t, &fakeModel{turns: []turn{
		answered(text("a draft")),
		critiqued(true, "improve it"),
		{err: errors.New("gateway down")},
	}})
	if result.Output != "a draft" || result.Steps != 3 {
		t.Errorf("Output %q in %d steps, want the draft in 3", result.Output, result.Steps)
	}
	skippedFor(t, skipped, "rewrite", 3, "rewriting", "cycle 1", "gateway down")
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

// reviewAskedTwice runs one cycle whose review is written out, then answered
// by second.
func reviewAskedTwice(t *testing.T, second turn) (*fakeModel, agent.Result, []agent.SkippedEvent) {
	t.Helper()
	model := &fakeModel{turns: []turn{answered(text("a draft")), answered(text("it is fine")), second}}
	result, skipped := reflectKept(t, model)
	return model, result, skipped
}

// Approval on the second ask ends the cycle as approval on the first does.
func TestASecondAskThatApprovesKeepsTheDraftWithoutARewrite(t *testing.T) {
	t.Parallel()

	model, result, skipped := reviewAskedTwice(t, critiqued(false, ""))
	if result.Output != "a draft" || len(model.calls) != 3 || len(skipped) != 0 {
		t.Errorf("Output %q after %d calls and skips %+v, want the draft after 3 and no skip",
			result.Output, len(model.calls), skipped)
	}
}

// The second ask fails as the first would: cut off, or refused, it keeps the
// draft and says why, and does not claim it was asked twice for nothing.
func TestASecondAskThatFailsKeepsTheDraftSayingWhy(t *testing.T) {
	t.Parallel()

	cut := answered(text("half a crit"))
	cut.resp.Finish = agent.FinishLength
	for name, c := range map[string]struct {
		second turn
		want   string
	}{
		"cut off": {cut, agent.ErrTruncated.Error()},
		"refused": {turn{err: errors.New("upstream 503")}, "upstream 503"},
	} {
		t.Run(name, func(t *testing.T) {
			_, result, skipped := reviewAskedTwice(t, c.second)
			if result.Output != "a draft" {
				t.Errorf("Output = %q, want the draft", result.Output)
			}
			skippedFor(t, skipped, "critique", 2, c.want)
			if strings.Contains(skipped[0].Reason, "asked twice") {
				t.Errorf("the reason %q says asked twice, but the second ask failed", skipped[0].Reason)
			}
		})
	}
}

// A second answer naming any other tool is no critique either, and there is
// no third ask.
func TestASecondAskCallingAnotherToolIsNoCritique(t *testing.T) {
	t.Parallel()

	model, result, skipped := reviewAskedTwice(t, answered(calling("web_search", `{}`)))
	if result.Output != "a draft" || len(model.calls) != 3 {
		t.Errorf("Output %q after %d calls, want the draft after 3", result.Output, len(model.calls))
	}
	skippedFor(t, skipped, "critique", 2, ErrNoCritique.Error(), "asked twice")
}

// A reply that only thought, or called something else, wrote no review: the
// second ask is the ask alone, with no empty fence and no stray call carried.
func TestAReviewThatWroteNothingIsAskedPlainly(t *testing.T) {
	t.Parallel()

	thought := agent.Message{Role: agent.RoleAssistant, Reasoning: &agent.Reasoning{Text: "hmm"}}
	stray := calling("web_search", `{"q": "berlin"}`)
	for name, first := range map[string]agent.Message{"only thought": thought, "called another tool": stray} {
		t.Run(name, func(t *testing.T) {
			model := &fakeModel{turns: []turn{answered(text("a draft")), answered(first), critiqued(false, "")}}
			reflectKept(t, model)
			if len(model.calls) != 3 {
				t.Fatalf("%d calls, want 3", len(model.calls))
			}
			msgs := model.calls[2].Messages
			added := msgs[len(msgs)-1]
			if added.Role != agent.RoleUser || added.Text() != submitNow {
				t.Errorf("the second ask ends %+v, want the ask alone", added)
			}
			for _, m := range msgs {
				if len(m.ToolCalls) > 0 || (m.Reasoning != nil && m.Reasoning.Text == "hmm") {
					t.Errorf("the second ask carries %+v from the first reply", m)
				}
			}
		})
	}
}

// Cancelled once the review came back written out, the run stops there: no
// second ask, and the cancellation, not a missing critique.
func TestACancelledRunIsNotAskedForItsReviewAgain(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	model := &fakeModel{turns: []turn{answered(text("a draft")), answered(text("it is fine"))}}
	model.turns[1].before = cancel
	_, err := Reflection(1).Run(ctx, agent.Input{Spec: reflectSpec(), Messages: ask("hello"), Model: model})
	if !errors.Is(err, context.Canceled) || len(model.calls) != 2 {
		t.Errorf("err = %v after %d calls, want the cancellation after 2", err, len(model.calls))
	}
}

// Each cycle asks again only if its own review was written out, and a cycle
// whose second ask fails keeps the rewrite of the cycle before.
func TestEachCycleAsksAgainOnlyForItsOwnReview(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(text("draft")),
		answered(text("needs work")), critiqued(true, "be shorter"), answered(text("rewrite 1")),
		answered(text("still long")), answered(text("I said it is long")),
	}}
	events := make(chan agent.Event, 64)
	result, err := Reflection(2).Run(t.Context(), agent.Input{
		Spec: reflectSpec(), Messages: ask("hello"), Model: model, Events: events,
	})
	close(events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "rewrite 1" || len(model.calls) != 6 {
		t.Errorf("Output %q after %d calls, want rewrite 1 after 6", result.Output, len(model.calls))
	}
	var steps []int
	var skipped []agent.SkippedEvent
	for e := range events {
		switch ev := e.(type) {
		case agent.StepEvent:
			steps = append(steps, ev.Step)
		case agent.SkippedEvent:
			skipped = append(skipped, ev)
		}
	}
	if want := []int{1, 2, 3, 4}; !slices.Equal(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	skippedFor(t, skipped, "critique", 4, "asked twice", "cycle 2")
}
