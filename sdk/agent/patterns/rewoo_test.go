package patterns

import (
	"context"
	"encoding/json"
	"errors"
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
// third would mean an observation reached the model mid-plan.
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

// A planning turn that answered without planning leaves nothing to execute,
// and no observation to recover from.
func TestAPlanningTurnWithNoPlanIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(text("I would rather just answer"))}}

	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, ErrNoPlan) {
		t.Fatalf("err = %v, want ErrNoPlan", err)
	}
	// And it says the plan is missing, not that some JSON would not parse.
	// Without its own check the empty call's nil arguments fail to unmarshal
	// and reach the same sentinel by accident, with an error naming neither
	// the plan nor the turn that should have carried it.
	if strings.Contains(err.Error(), "JSON") {
		t.Errorf("err = %v, want it to report a missing plan rather than a parse failure", err)
	}
	if len(model.calls) != 1 {
		t.Error("the run continued past a turn with no plan")
	}
}

func TestAnEmptyPlanIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{submitted(`[]`)}}
	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, ErrEmptyPlan) {
		t.Errorf("err = %v, want ErrEmptyPlan", err)
	}
}

func TestAPlanWithUnreadableArgumentsIsRefused(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(calling(PlanToolName, `{"steps":`))}}
	_, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, ErrNoPlan) {
		t.Errorf("err = %v, want ErrNoPlan", err)
	}
}

// Here MaxSteps caps tool calls rather than model turns, because the model is
// called twice whatever happens and a ceiling on that bounds nothing.
func TestAPlanLongerThanTheCeilingIsRefused(t *testing.T) {
	t.Parallel()

	s := rewooSpec(tool("count", "3", nil, nil))
	s.MaxSteps = 2

	model := &fakeModel{turns: []turn{submitted(`[
		{"tool":"count","args":{},"why":"a"},
		{"tool":"count","args":{},"why":"b"},
		{"tool":"count","args":{},"why":"c"}]`)}}

	_, err := ReWOO().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: model})
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if !strings.Contains(err.Error(), "3 steps") {
		t.Errorf("err = %v, want it to say how long the plan was", err)
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

// A truncated plan is a plan missing its last steps, and nothing downstream
// would know they were meant to exist.
func TestATruncatedPlanStopsTheRunBeforeAnythingRuns(t *testing.T) {
	t.Parallel()

	var ran bool
	model := &fakeModel{turns: []turn{{resp: agent.Response{
		Message: calling(PlanToolName, `{"steps":[{"tool":"count","args":{},"why":"a"}]}`),
		Finish:  agent.FinishLength,
		Usage:   agent.Usage{InputTokens: 10, OutputTokens: 5},
	}}}}

	result, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, &ran)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if ran {
		t.Error("a truncated plan was executed")
	}
	if result.Steps != 1 {
		t.Errorf("Steps = %d, want the planning step", result.Steps)
	}
}

// A truncated answer is still an error, the same as in every other loop.
func TestATruncatedAnswerIsReported(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"count","args":{},"why":"a"}]`),
		{resp: agent.Response{Message: text("the count is"), Finish: agent.FinishLength}},
	}}

	result, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(tool("count", "3", nil, nil)), Messages: ask("go"), Model: model,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if result.Output != "the count is" {
		t.Errorf("Output = %q, want what did arrive", result.Output)
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
