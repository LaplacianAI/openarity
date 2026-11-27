package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func rewooSpec(tools ...agent.Tool) agent.Spec {
	s := spec(tools...)
	s.Pattern = agent.PatternReWOO
	return s
}

// submitted builds the assistant turn the planner produces: one call to the
// plan tool carrying the steps.
func submitted(steps string) turn {
	return answered(calling(PlanToolName, `{"steps":`+steps+`}`))
}

func TestReWOOAnswersItsOwnName(t *testing.T) {
	t.Parallel()

	if got := ReWOO().Name(); got != agent.PatternReWOO {
		t.Errorf("Name() = %q, want %q", got, agent.PatternReWOO)
	}
	if got := ReWOOStreaming().Name(); got != agent.PatternReWOO {
		t.Errorf("streaming Name() = %q, want %q", got, agent.PatternReWOO)
	}
}

// The whole claim of the pattern: two model calls however many tools run. A
// third on a readable plan would mean an observation reached the model
// mid-plan; only a re-ask of an unreadable plan costs one.
func TestThreeToolsCostTwoModelCalls(t *testing.T) {
	t.Parallel()

	var ran int
	count := agent.Tool{
		Name: "count", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			ran++
			return "counted " + string(args), nil
		},
	}

	model := &fakeModel{turns: []turn{
		submitted(`[
			{"tool":"count","args":{"repo":"brain"},"why":"a"},
			{"tool":"count","args":{"repo":"cli"},"why":"b"},
			{"tool":"count","args":{"repo":"sdk"},"why":"c"}]`),
		answered(text("three repositories counted")),
	}}

	result, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(count), Messages: ask("count them all"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.calls) != 2 {
		t.Errorf("the model was called %d times, want 2 whatever the plan costs", len(model.calls))
	}
	if ran != 3 {
		t.Errorf("the tool ran %d times, want 3", ran)
	}
	if result.Output != "three repositories counted" {
		t.Errorf("Output = %q", result.Output)
	}
	if result.Steps != 2 {
		t.Errorf("Steps = %d, want 2", result.Steps)
	}
}

// The planner writes down calls rather than making them, so it gets one tool
// and a description of the rest. Handing it the real ones would let it act.
func TestThePlanningCallGetsOnlyThePlanTool(t *testing.T) {
	t.Parallel()

	count := tool("count", "3", nil, nil)
	count.Description = "Count the issues in a repository"

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("done")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(count), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	planning := model.calls[0]
	if len(planning.Tools) != 1 || planning.Tools[0].Name != PlanToolName {
		t.Fatalf("the planning call carried %d tools, want only %s", len(planning.Tools), PlanToolName)
	}

	// And the real tools reached it as a description instead.
	var described string
	for _, c := range planning.System {
		described += c.Text
	}
	for _, want := range []string{"count", "Count the issues in a repository", "#E1"} {
		if !strings.Contains(described, want) {
			t.Errorf("the catalogue does not mention %q:\n%s", want, described)
		}
	}
}

// Offering tools to the solving call lets the model start acting again, which
// is the loop this pattern exists to avoid.
func TestTheSolvingCallGetsNoTools(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("done")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(model.calls[1].Tools); n != 0 {
		t.Errorf("the solving call carried %d tools, want none", n)
	}
}

// A step's output reaches the next step by substitution, which is the only
// thing that makes a plan written in advance able to chain.
func TestAStepReadsAnEarlierStepsOutput(t *testing.T) {
	t.Parallel()

	var saw string
	echo := agent.Tool{
		Name: "echo", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(args, &in)
			if in.Text != "" {
				saw = in.Text
			}
			return "the answer is 42", nil
		},
	}

	model := &fakeModel{turns: []turn{
		submitted(`[
			{"tool":"echo","args":{},"why":"find it"},
			{"tool":"echo","args":{"text":"#E1"},"why":"use it"}]`),
		answered(text("42")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(echo), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if saw != "the answer is 42" {
		t.Errorf("the second step saw %q, want the first step's output", saw)
	}
}

// A placeholder inside a sentence is spliced rather than replacing the whole
// value, because that is how a model writes one.
func TestAPlaceholderInsideASentenceIsSpliced(t *testing.T) {
	t.Parallel()

	got, err := substitute(json.RawMessage(`{"text":"the count was #E1 today"}`), []string{"three"})
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if !strings.Contains(string(got), "the count was three today") {
		t.Errorf("substitute() = %s", got)
	}
}

// Only strings are rewritten. A number or a key that happened to look like a
// reference is data, and changing it would corrupt the call.
func TestSubstitutionLeavesNonStringsAlone(t *testing.T) {
	t.Parallel()

	got, err := substitute(json.RawMessage(`{"n":1,"ok":true,"nested":{"deep":["#E1",2]}}`), []string{"x"})
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	if doc["n"] != float64(1) || doc["ok"] != true {
		t.Errorf("a non-string value was rewritten: %s", got)
	}
	nested, _ := doc["nested"].(map[string]any)
	deep, _ := nested["deep"].([]any)
	if len(deep) != 2 || deep[0] != "x" || deep[1] != float64(2) {
		t.Errorf("nested substitution = %v", deep)
	}
}

// Leaving a forward reference in place would send the literal text "#E4" to a
// tool as though it were data, and the tool would answer about nothing.
func TestAForwardReferenceIsRefusedRatherThanSentAsText(t *testing.T) {
	t.Parallel()

	_, err := substitute(json.RawMessage(`{"text":"#E4"}`), []string{"only one"})
	if err == nil {
		t.Fatal("a reference to a step that had not run was accepted")
	}
	if !strings.Contains(err.Error(), "#E4") {
		t.Errorf("err = %v, want it to name the step", err)
	}
}

// Ten or more steps: #E1 must not be substituted inside #E12 first. The loop
// replaces from the highest index down for exactly this reason.
func TestATwoDigitReferenceIsNotEatenByASingleDigitOne(t *testing.T) {
	t.Parallel()

	evidence := make([]string, 12)
	for i := range evidence {
		evidence[i] = "step" + string(rune('a'+i))
	}

	got, err := substitute(json.RawMessage(`{"text":"#E12"}`), evidence)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if !strings.Contains(string(got), "stepl") {
		t.Errorf("substitute() = %s, want step 12's output", got)
	}
}

// A step that fails records why and the plan continues. There is no model in
// the loop to recover, so the solving call is the only thing that can, and it
// needs to see what went wrong.
func TestAFailedStepBecomesEvidenceAndThePlanContinues(t *testing.T) {
	t.Parallel()

	var lateRan bool
	broken := agent.Tool{
		Name: "broken", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("the index is offline")
		},
	}
	late := tool("late", "ran anyway", nil, &lateRan)

	model := &fakeModel{turns: []turn{
		submitted(`[
			{"tool":"broken","args":{},"why":"a"},
			{"tool":"late","args":{},"why":"b"}]`),
		answered(text("partly answered")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(broken, late), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !lateRan {
		t.Error("a failing step stopped the plan")
	}

	evidence := lastToolMessage(t, model.calls[1].Messages)
	if !strings.Contains(evidence, "the index is offline") {
		t.Errorf("the failure did not reach the solving call:\n%s", evidence)
	}
}

// A plan naming a tool this run was never offered must not end the run either.
func TestAnUnknownToolInThePlanBecomesEvidence(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"deploy","args":{},"why":"a"}]`),
		answered(text("could not do it")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	evidence := lastToolMessage(t, model.calls[1].Messages)
	if !strings.Contains(evidence, "deploy") {
		t.Errorf("the unknown tool was not reported:\n%s", evidence)
	}
}

// The plan is an assistant tool call, so the transcript needs a message
// answering it. Without one the solving request is rejected by the gateway.
func TestTheEvidenceAnswersThePlansToolCall(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("done")),
	}}

	result, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := model.calls[1].Messages
	if len(msgs) != 3 {
		t.Fatalf("the solving call carried %d messages, want the question, the plan and the evidence", len(msgs))
	}
	if msgs[1].Role != agent.RoleAssistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("the second message is not the plan's tool call: %+v", msgs[1])
	}
	if msgs[2].Role != agent.RoleTool {
		t.Fatalf("the plan's tool call is not answered: %+v", msgs[2])
	}
	if msgs[2].ToolCallID != msgs[1].ToolCalls[0].ID {
		t.Errorf("the answer names call %q, want %q", msgs[2].ToolCallID, msgs[1].ToolCalls[0].ID)
	}
	if len(result.Messages) != 4 {
		t.Errorf("the result holds %d messages, want four", len(result.Messages))
	}
}

// A planning turn that finds nothing to plan, as a greeting has, is answered
// as after a plan with no evidence: no tool runs, and the run does not fail.
func TestNothingToPlanIsAnsweredWithoutTools(t *testing.T) {
	t.Parallel()

	cases := map[string]turn{
		"no plan submitted":  answered(text("Hi! How can I help?")),
		"a plan of no steps": submitted(`[]`),
	}
	for name, planning := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ran := false
			count := tool("count", "3", nil, &ran)
			model := &fakeModel{turns: []turn{planning, answered(text("Hello."))}}

			result, err := ReWOO().Run(t.Context(), agent.Input{
				Spec: rewooSpec(count), Messages: ask("Hi"), Model: model,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Output != "Hello." || ran || len(model.calls) != 2 {
				t.Errorf("output %q, tool ran %v, model called %d; want the solver's answer, no tool, two calls",
					result.Output, ran, len(model.calls))
			}
			// The solver sees the conversation, not a planning turn that was
			// never shown: an unanswered tool call there is a request refused.
			for _, m := range model.calls[1].Messages {
				if len(m.ToolCalls) > 0 {
					t.Errorf("the solver was sent a tool call: %+v", m)
				}
			}
		})
	}
}

// rewooKept runs ReWOO against model and returns its answer, the skips it
// reported and the calls it made.
func rewooKept(t *testing.T, s agent.Spec, model *fakeModel) (agent.Result, []agent.SkippedEvent) {
	t.Helper()
	events := make(chan agent.Event, 64)
	result, err := ReWOO().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model, Events: events})
	close(events)
	if err != nil {
		t.Fatalf("Run: %v, want it answered without the plan", err)
	}
	var skipped []agent.SkippedEvent
	for e := range events {
		if sk, ok := e.(agent.SkippedEvent); ok {
			skipped = append(skipped, sk)
		}
	}
	return result, skipped
}

// A plan the model wrote and the pattern cannot use is answered as no plan:
// the solver answers with no evidence, nothing is run, and the skip says why.
func TestAnUnusablePlanIsAnsweredWithoutOne(t *testing.T) {
	t.Parallel()

	cut := turn{resp: agent.Response{
		Message: calling(PlanToolName, `{"steps":[{"tool":"count","args":{},"why":"a"}]}`),
		Finish:  agent.FinishLength,
	}}
	for name, c := range map[string]struct {
		plan     turn
		maxSteps int
		want     string
		asks     int // planning calls: an unreadable plan is asked for again; a plan too long or cut off is not
	}{
		"unreadable": {answered(calling(PlanToolName, `{"steps":`)), 8, ErrNoPlan.Error(), 2},
		// Here MaxSteps caps tool calls rather than model turns, because the
		// model is called twice whatever happens.
		"too long": {submitted(`[{"tool":"count","args":{},"why":"a"},{"tool":"count","args":{},"why":"b"},{"tool":"count","args":{},"why":"c"}]`), 2, "3 steps", 1},
		// A truncated plan is missing its last steps, and nothing downstream
		// would know they were meant to exist.
		"cut off": {cut, 8, agent.ErrTruncated.Error(), 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var ran bool
			s := rewooSpec(tool("count", "3", nil, &ran))
			s.MaxSteps = c.maxSteps
			model := &fakeModel{turns: append(slices.Repeat([]turn{c.plan}, c.asks), answered(text("the answer")))}

			result, skipped := rewooKept(t, s, model)
			if result.Output != "the answer" || result.Steps != 2 {
				t.Errorf("Output %q in %d steps, want the solver's answer in 2", result.Output, result.Steps)
			}
			if ran {
				t.Error("an unusable plan was carried out")
			}
			if len(skipped) != 1 || skipped[0].What != "plan" || skipped[0].Step != 1 ||
				!strings.Contains(skipped[0].Reason, c.want) {
				t.Errorf("skipped %+v, want the plan at step 1, saying %q", skipped, c.want)
			}
			if want := c.asks + 1; len(model.calls) != want || len(model.calls[want-1].Tools) != 0 {
				t.Errorf("%d calls, the solver offered %d tools; want %d and none", len(model.calls), len(model.calls[len(model.calls)-1].Tools), want)
			}
		})
	}
}

func TestReWOOWithNoCeilingIsRefused(t *testing.T) {
	t.Parallel()

	s := rewooSpec(tool("count", "3", nil, nil))
	s.MaxSteps = 0

	model := &fakeModel{turns: []turn{submitted(`[{"tool":"count","args":{},"why":"a"}]`)}}
	_, err := ReWOO().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model})
	if !errors.Is(err, agent.ErrNoMaxSteps) {
		t.Fatalf("err = %v, want ErrNoMaxSteps", err)
	}
	if len(model.calls) != 0 {
		t.Error("a refused run still called the model")
	}
}

// A run with nothing to plan with would spend a model call to be told so.
func TestASpecWithNoToolsIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(text("hello"))}}
	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(), Messages: ask("go"), Model: model,
	})
	if err == nil {
		t.Fatal("a spec with no tools was accepted")
	}
	if len(model.calls) != 0 {
		t.Error("a refused run still called the model")
	}
}

func TestReWOOWithNoModelClientIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := ReWOO().Run(t.Context(), agent.Input{Spec: rewooSpec(tool("c", "", nil, nil))}); err == nil {
		t.Error("a run with no ModelClient was accepted")
	}
}

// An answer cut off every time it goes on is still an error, the same as in
// every other loop.
func TestAnAnswerCutOffEveryTimeIsReported(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: append(
		[]turn{submitted(`[{"tool":"count","args":{},"why":"a"}]`)},
		cutTurns("the count is", 1+continuations)...)}

	result, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if want := strings.Repeat("the count is", 1+continuations); result.Output != want {
		t.Errorf("Output = %q, want all that did arrive, %q", result.Output, want)
	}
}

func TestReWOOSumsBothCallsTokens(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		{resp: agent.Response{
			Message: calling(PlanToolName, `{"steps":[{"tool":"count","args":{},"why":"a"}]}`),
			Finish:  agent.FinishToolCalls,
			Usage:   agent.Usage{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2},
		}},
		{resp: agent.Response{
			Message: text("3"), Finish: agent.FinishStop,
			Usage: agent.Usage{InputTokens: 20, OutputTokens: 6, CachedInputTokens: 3},
		}},
	}}

	result := through(t, ReWOO(), rewooSpec(tool("count", "3", nil, nil)), ask("go"), model)

	want := agent.Usage{InputTokens: 30, OutputTokens: 10, CachedInputTokens: 5}
	if result.Usage != want {
		t.Errorf("Usage = %+v, want %+v", result.Usage, want)
	}
}

// Cancelling during a tool ends the run. There is no model in the loop to
// notice, so this check is the only thing that stops the rest of the plan.
func TestCancellationDuringAStepEndsTheRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	var secondRan bool

	first := agent.Tool{
		Name: "first", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			cancel()
			return "done", nil
		},
	}
	second := tool("second", "also done", nil, &secondRan)

	model := &fakeModel{turns: []turn{submitted(`[
		{"tool":"first","args":{},"why":"a"},
		{"tool":"second","args":{},"why":"b"}]`)}}

	_, err := ReWOO().Run(ctx, agent.Input{
		Spec: rewooSpec(first, second), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if secondRan {
		t.Error("the plan kept running after the context was cancelled")
	}
}

// A batch run passes no channel, and every phase must cope.
func TestReWOOWithNilEventsIsNotAnError(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("done")),
	}}
	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Errorf("Run: %v", err)
	}
}

// Every step is reported, so a dashboard sees the plan running even though the
// model is not involved while it does.
func TestEveryStepOfThePlanIsReported(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[
			{"tool":"count","args":{},"why":"a"},
			{"tool":"count","args":{},"why":"b"}]`),
		answered(text("done")),
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var calls, results, steps int
	go func() {
		defer close(done)
		for e := range events {
			switch e.(type) {
			case agent.ToolCallEvent:
				calls++
			case agent.ToolResultEvent:
				results++
			case agent.StepEvent:
				steps++
			}
		}
	}()

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"),
		Model: model, Events: events,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)
	<-done

	if calls != 2 || results != 2 {
		t.Errorf("%d tool calls and %d results reported, want 2 and 2", calls, results)
	}
	if steps != 2 {
		t.Errorf("%d steps reported, want 2 — planning and solving", steps)
	}
}

// The streaming constructor streams the answer. The plan is a tool call and
// half a plan is not readable, so that call stays whole.
func TestReWOOStreamingStreamsTheAnswer(t *testing.T) {
	t.Parallel()

	model := &fakeModel{
		deltas: []string{"three ", "issues"},
		turns: []turn{
			submitted(`[{"tool":"count","args":{},"why":"a"}]`),
			answered(text("three issues")),
		},
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

	result, err := ReWOOStreaming().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"),
		Model: model, Events: events,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)
	<-done

	if len(deltas) != 2 || deltas[0] != "three " {
		t.Errorf("deltas = %v, want the answer's fragments", deltas)
	}
	if result.Output != "three issues" {
		t.Errorf("Output = %q", result.Output)
	}
}

// The plan tool is read off the tool call, never dispatched. A body that ran
// would execute before the plan had been checked at all.
func TestThePlanToolRefusesToBeInvoked(t *testing.T) {
	t.Parallel()

	pt := planTool([]agent.Tool{{Name: "count"}})
	if _, err := pt.Invoke(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Error("the plan tool ran when it was called")
	}

	var schema struct {
		Properties struct {
			Steps struct {
				Items struct {
					Properties struct {
						Tool struct {
							Enum []string `json:"enum"`
						} `json:"tool"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"steps"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(pt.Schema, &schema); err != nil {
		t.Fatalf("the schema is not valid JSON: %v", err)
	}
	if got := schema.Properties.Steps.Items.Properties.Tool.Enum; len(got) != 1 || got[0] != "count" {
		t.Errorf("the enum is %v, want the run's tool names", got)
	}
}

// Replacing the system prompt would drop the agent's identity and every
// guideline the deployment set, in both phases.
func TestBothPhasesAppendToTheSystemPrompt(t *testing.T) {
	t.Parallel()

	s := rewooSpec(tool("count", "3", nil, nil))
	// Spare capacity on purpose: a caller whose slice is full gets a
	// reallocation from append and a missing clone cannot reproduce.
	s.System = make([]agent.Content, 1, 4)
	s.System[0] = agent.Content{Type: agent.ContentText, Text: "You are a terse assistant."}

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("done")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for i, call := range model.calls {
		if len(call.System) != 2 {
			t.Errorf("call %d carried %d system blocks, want the caller's plus this phase's", i, len(call.System))
			continue
		}
		if call.System[0].Text != "You are a terse assistant." {
			t.Errorf("call %d lost the caller's prompt: %q", i, call.System[0].Text)
		}
	}

	if len(s.System) != 1 {
		t.Fatal("the caller's system prompt grew")
	}
	if got := s.System[:2][1].Text; got != "" {
		t.Errorf("a phase wrote into the caller's array as %q", got)
	}
}

func lastToolMessage(t *testing.T, msgs []agent.Message) string {
	t.Helper()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == agent.RoleTool {
			return msgs[i].Text()
		}
	}
	t.Fatal("no tool message reached the solving call")
	return ""
}

// A model that fails on the planning call must say which phase failed. An
// unqualified error would leave a reader guessing between two calls that look
// nothing alike.
func TestAFailedReWOOPlanningCallNamesThePhase(t *testing.T) {
	t.Parallel()

	boom := errors.New("the gateway refused")
	model := &fakeModel{turns: []turn{{err: boom}}}

	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the gateway's error", err)
	}
	if !strings.Contains(err.Error(), "planning") {
		t.Errorf("err = %v, want it to name the phase", err)
	}
}

func TestAFailedSolvingCallNamesThePhase(t *testing.T) {
	t.Parallel()

	boom := errors.New("the gateway refused")
	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		{err: boom},
	}}

	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the gateway's error", err)
	}
	if !strings.Contains(err.Error(), "solving") {
		t.Errorf("err = %v, want it to name the phase", err)
	}
}

// A forward reference is caught while the plan runs, and the step that could
// not be built becomes evidence like any other failure.
func TestAForwardReferenceInAPlanBecomesEvidence(t *testing.T) {
	t.Parallel()

	var ran bool
	model := &fakeModel{turns: []turn{
		submitted(`[
			{"tool":"count","args":{"text":"#E2"},"why":"too early"},
			{"tool":"count","args":{},"why":"the one it wanted"}]`),
		answered(text("could not do the first")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, &ran)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	evidence := lastToolMessage(t, model.calls[1].Messages)
	if !strings.Contains(evidence, "#E2") {
		t.Errorf("the forward reference was not reported:\n%s", evidence)
	}
	if !ran {
		t.Error("a step that could not be built stopped the plan")
	}
}

// A step the model wrote with no args at all must reach the tool as an empty
// object. Sent as null, json.Unmarshal into a struct accepts it silently and
// the tool runs with every field zeroed instead of failing.
func TestAStepWithNoArgumentsSendsAnEmptyObject(t *testing.T) {
	t.Parallel()

	var saw string
	watcher := agent.Tool{
		Name: "count", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			saw = string(args)
			return "3", nil
		},
	}

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","why":"no args at all"}]`),
		answered(text("3")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(watcher), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if saw != "{}" {
		t.Errorf("the tool was called with %q, want an empty object", saw)
	}
}

// Arguments the model wrote as something other than an object cannot be
// substituted into, and the step says so rather than reaching a tool that
// expects one.
func TestAStepWithUnreadableArgumentsBecomesEvidence(t *testing.T) {
	t.Parallel()

	var ran bool
	model := &fakeModel{turns: []turn{
		answered(calling(PlanToolName, `{"steps":[{"tool":"count","args":"not an object","why":"a"}]}`)),
		answered(text("could not")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, &ran)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran {
		t.Error("a step with unreadable arguments reached the tool")
	}
	if evidence := lastToolMessage(t, model.calls[1].Messages); !strings.Contains(evidence, "not an object") {
		t.Errorf("the failure was not reported:\n%s", evidence)
	}
}

// Substitution walks into arrays as well as objects, and a bad reference
// anywhere inside one fails the whole step rather than half-rewriting it.
func TestABadReferenceInsideAnArrayFailsTheStep(t *testing.T) {
	t.Parallel()

	if _, err := substitute(json.RawMessage(`{"list":["fine","#E9"]}`), []string{"one"}); err == nil {
		t.Error("a forward reference nested in an array was accepted")
	}

	got, err := substitute(json.RawMessage(`{"list":["#E1","plain"]}`), []string{"filled"})
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if !strings.Contains(string(got), `["filled","plain"]`) {
		t.Errorf("substitute() = %s", got)
	}
}

// Both phases carry the conversation. The planning call cannot plan without
// knowing what was asked, and the solving call answers the question rather
// than the evidence.
func TestReWOOCarriesTheConversationIntoBothPhases(t *testing.T) {
	t.Parallel()

	prior := history()
	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{"repo":"brain"},"why":"a"}]`),
		answered(text("3")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "brain has 3 open issues", nil, nil)), Messages: prior, Model: model,
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

// systemText is every system block of a call, joined.
func systemText(req agent.Request) string {
	var b strings.Builder
	for _, c := range req.System {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// A plan whose steps were sent as a JSON string, as some models send an
// array argument, is read as the list it holds and carried out.
func TestStepsSentAsAStringAreCarriedOut(t *testing.T) {
	t.Parallel()

	var ran bool
	model := &fakeModel{turns: []turn{
		submitted(`"[{\"tool\":\"count\",\"args\":{},\"why\":\"a\"}]"`),
		answered(text("there are 3")),
	}}
	result, skipped := rewooKept(t, rewooSpec(tool("count", "3", nil, &ran)), model)
	if !ran || result.Output != "there are 3" || len(skipped) != 0 || len(model.calls) != 2 {
		t.Errorf("ran %v, output %q, skipped %+v, %d calls; want the plan carried out in two", ran, result.Output, skipped, len(model.calls))
	}
}

// A plan that cannot be read is asked for once more, with why: the second
// plan is carried out, and the refused call is not sent back.
func TestAnUnreadablePlanIsAskedForAgain(t *testing.T) {
	t.Parallel()

	var ran bool
	model := &fakeModel{turns: []turn{
		submitted(`{"tool":"count"}`), // an object, not a list
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		answered(text("there are 3")),
	}}
	result, skipped := rewooKept(t, rewooSpec(tool("count", "3", nil, &ran)), model)
	if !ran || result.Output != "there are 3" || len(skipped) != 0 || len(model.calls) != 3 {
		t.Fatalf("ran %v, output %q, skipped %+v, %d calls; want the second plan carried out", ran, result.Output, skipped, len(model.calls))
	}
	again := model.calls[1].Messages
	last := again[len(again)-1]
	if len(again) != 1 || last.Role != agent.RoleUser || !strings.Contains(last.Text(), "go") ||
		!strings.Contains(last.Text(), "Your plan could not be read") {
		t.Errorf("the second planning call was sent %d messages ending %+v; want the person's, with the note added", len(again), last)
	}
	for _, leak := range []string{"json:", "patterns.step", "Go struct"} {
		if strings.Contains(last.Text(), leak) {
			t.Errorf("the note quotes the parser: %q", last.Text())
		}
	}
	if strings.Contains(textOf(result.Messages), "could not be read") {
		t.Errorf("the note was kept in the run's messages")
	}
	for _, m := range again {
		if len(m.ToolCalls) > 0 {
			t.Errorf("the refused call was sent back: %+v", m)
		}
	}
}

// When no plan ran, the answer step is told so, never that the plan ran:
// two unreadable plans, and a planning turn that planned nothing.
func TestWithNoPlanTheAnswerIsToldNothingRan(t *testing.T) {
	t.Parallel()

	for name, turns := range map[string][]turn{
		"two unreadable plans": {submitted(`"not a list"`), submitted(`"still not"`)},
		"nothing to plan":      {answered(text("Hi!"))},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := &fakeModel{turns: append(turns, answered(text("Hello.")))}
			result, _ := rewooKept(t, rewooSpec(tool("count", "3", nil, new(bool))), model)
			solver := systemText(model.calls[len(model.calls)-1])
			if result.Output != "Hello." || !strings.Contains(solver, "No tool ran") || strings.Contains(solver, "The plan has run") {
				t.Errorf("output %q; the answer step was told:\n%s", result.Output, solver)
			}
		})
	}
}

// An answer that ends in a tool call written as text fails the run, after a
// plan and with none, and leaves nothing of it in the output or the turns a
// runner would save; prose that quotes a call and goes on is an answer.
func TestAnAnswerEndingInAToolCallFailsTheRun(t *testing.T) {
	t.Parallel()

	markup := "\n<invoke name=\"Skill\">\n<parameter name=\"skill\">caveman</parameter>\n</invoke>"
	for name, c := range map[string]struct {
		planned bool
		answer  string
		fails   bool
	}{
		"markup, no plan":               {false, markup, true},
		"markup after a plan":           {true, markup, true},
		"wrapped markup":                {false, "<function_calls>" + markup + "</function_calls>", true},
		"words, then a call":            {false, "Let me load the skill first." + markup, true},
		"a call quoted, then explained": {false, "It printed this:" + markup + "\nwhich is a tool call written as text.", false},
		"a plain answer":                {true, "Hello.", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan := answered(text("nothing to plan"))
			if c.planned {
				plan = submitted(`[{"tool":"count","args":{},"why":"a"}]`)
			}
			model := &fakeModel{turns: []turn{plan, answered(text(c.answer))}}
			result, err := ReWOO().Run(t.Context(), agent.Input{Spec: rewooSpec(tool("count", "3", nil, new(bool))), Messages: ask("go"), Model: model})
			if got := errors.Is(err, ErrToolMarkup); got != c.fails {
				t.Fatalf("err %v, want ErrToolMarkup %v", err, c.fails)
			}
			if !c.fails {
				return
			}
			if result.Output != "" || strings.Contains(textOf(result.Messages), "<invoke") || result.Steps != 2 {
				t.Errorf("output %q in %d steps, messages %q; want nothing of the markup kept", result.Output, result.Steps, textOf(result.Messages))
			}
		})
	}
}

func TestEndsInToolCall(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		"<invoke name=\"a\"><parameter name=\"p\">1</parameter></invoke>": true,
		"<function_calls><invoke name=\"a\"></invoke></function_calls>":   true,
		"<tool_call>{\"name\":\"a\"}</tool_call>":                         true,
		"<invoke name=\"a\">x</tool_call>":                                true,
		"<invoke name=\"a\">\n<parameter name=\"p\">cut off her":          true,
		"<invoke name=\"a\">":                                             true,
		"<invoke name=\"a":                                                true,
		"<invoke name=\"a\"></invoke>\n<invoke name=\"b\">":               true,
		"I will look it up.\n<invoke name=\"a\"></invoke>":                true,
		"<invoke> is the tag. Use it.":                                    false,
		"the <tool_call> element":                                         false,
		"<invoke name=\"a\">x and words":                                  false,
		"<invokes>x</invokes>":                                            false,
		"<invoke name=\"a\"></invoke> is how a call looks.":               false,
		"":       false,
		"Hello.": false,
	} {
		if got := endsInToolCall(text); got != want {
			t.Errorf("endsInToolCall(%q) = %v, want %v", text, got, want)
		}
	}
}

// A plan's steps: a list, the list as a string, or none.
func TestParseSteps(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]int{
		`[{"tool":"a","args":{},"why":"x"}]`:             1,
		`"[{\"tool\":\"a\",\"args\":{},\"why\":\"x\"}]"`: 1,
		`[]`:   0,
		`"[]"`: 0,
		`null`: 0,
		``:     0,
	} {
		got, err := parseSteps(json.RawMessage(raw))
		if err != nil || len(got) != want {
			t.Errorf("parseSteps(%s) = %d steps, %v; want %d", raw, len(got), err, want)
		}
	}
	for _, raw := range []string{`""`, `"not a list"`, `{"tool":"a"}`, `"\"[]\""`, `3`} {
		if _, err := parseSteps(json.RawMessage(raw)); err == nil {
			t.Errorf("parseSteps(%s) read a plan", raw)
		}
	}
}

// The second ask is the only one: one that fails outright is the run's
// failure, one that plans nothing goes to the answer, and one too long is
// answered as no plan.
func TestTheSecondAskIsTheLast(t *testing.T) {
	t.Parallel()

	bad := submitted(`"not a list"`)
	t.Run("it fails", func(t *testing.T) {
		t.Parallel()
		model := &fakeModel{turns: []turn{bad, {err: errors.New("gateway down")}}}
		_, err := ReWOO().Run(t.Context(), agent.Input{Spec: rewooSpec(tool("count", "3", nil, new(bool))), Messages: ask("go"), Model: model})
		if err == nil || !strings.Contains(err.Error(), "planning:") || len(model.calls) != 2 {
			t.Errorf("err %v after %d calls; want the planning failure, no third ask", err, len(model.calls))
		}
	})
	for name, second := range map[string]turn{
		"it plans nothing": answered(text("Hi!")),
		"it is too long":   submitted(`[{"tool":"count","args":{},"why":"a"},{"tool":"count","args":{},"why":"b"},{"tool":"count","args":{},"why":"c"}]`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var ran bool
			s := rewooSpec(tool("count", "3", nil, &ran))
			s.MaxSteps = 2
			model := &fakeModel{turns: []turn{bad, second, answered(text("Hello."))}}
			result, _ := rewooKept(t, s, model)
			if ran || result.Output != "Hello." || len(model.calls) != 3 || !strings.Contains(systemText(model.calls[2]), "No tool ran") {
				t.Errorf("ran %v, output %q after %d calls; want the no-plan answer on the third", ran, result.Output, len(model.calls))
			}
		})
	}
}

// textOf is every message's text, joined.
func textOf(msgs []agent.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}
