package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// fakeModel returns canned turns in order. Everything the pattern decides —
// whether to dispatch, what to append, when to stop — happens between these
// turns, so a fake is enough to drive every branch without a network.
type fakeModel struct {
	turns []turn
	calls []agent.Request

	// streamed[i] records whether call i went through Stream rather than
	// Complete. A pattern that streams a turn whose whole answer is a tool
	// call shows the caller nothing while appearing to stall.
	streamed []bool

	// deltas are the text fragments Stream yields before the final response.
	deltas []string

	// noFinal makes Stream end without a final event, which is what a broken
	// client looks like.
	noFinal bool

	// streamErr is what Err reports once the events run out — a connection
	// that dropped part-way rather than a turn that finished.
	streamErr error
}

type turn struct {
	resp agent.Response
	err  error
}

func (m *fakeModel) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	m.calls = append(m.calls, req)
	m.streamed = append(m.streamed, false)
	if len(m.turns) == 0 {
		return agent.Response{}, errors.New("the pattern asked for more turns than the fake has")
	}
	t := m.turns[0]
	m.turns = m.turns[1:]
	return t.resp, t.err
}

func (m *fakeModel) Stream(_ context.Context, req agent.Request) (agent.Stream, error) {
	m.calls = append(m.calls, req)
	m.streamed = append(m.streamed, true)
	if len(m.turns) == 0 {
		return nil, errors.New("the pattern asked for more turns than the fake has")
	}
	t := m.turns[0]
	m.turns = m.turns[1:]
	if t.err != nil {
		return nil, t.err
	}

	events := make([]agent.StreamEvent, 0, len(m.deltas)+1)
	for _, d := range m.deltas {
		events = append(events, agent.StreamEvent{Text: d})
	}
	if !m.noFinal {
		final := t.resp
		events = append(events, agent.StreamEvent{Final: &final})
	}
	return &fakeStream{events: events, err: m.streamErr}, nil
}

type fakeStream struct {
	events []agent.StreamEvent
	cur    agent.StreamEvent
	err    error
	closed bool
}

func (s *fakeStream) Next() bool {
	if len(s.events) == 0 {
		return false
	}
	s.cur = s.events[0]
	s.events = s.events[1:]
	return true
}

func (s *fakeStream) Event() agent.StreamEvent { return s.cur }
func (s *fakeStream) Err() error               { return s.err }
func (s *fakeStream) Close() error             { s.closed = true; return nil }

func text(s string) agent.Message {
	return agent.Message{
		Role:    agent.RoleAssistant,
		Content: []agent.Content{{Type: agent.ContentText, Text: s}},
	}
}

func calling(name, args string) agent.Message {
	return agent.Message{
		Role:      agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{{ID: "call_1", Name: name, Arguments: json.RawMessage(args)}},
	}
}

func answered(msg agent.Message) turn {
	finish := agent.FinishStop
	if len(msg.ToolCalls) > 0 {
		finish = agent.FinishToolCalls
	}
	return turn{resp: agent.Response{Message: msg, Finish: finish}}
}

func spec(tools ...agent.Tool) agent.Spec {
	return agent.Spec{
		Model:    agent.ModelRef{Name: "probe"},
		Pattern:  agent.PatternReAct,
		Tools:    tools,
		MaxSteps: 5,
	}
}

func ask(s string) []agent.Message {
	return []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: s}},
	}}
}

// tool builds a Tool whose Invoke records that it ran.
func tool(name, out string, err error, ran *bool) agent.Tool {
	return agent.Tool{
		Name:   name,
		Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			if ran != nil {
				*ran = true
			}
			return out, err
		},
	}
}

func TestAnAnswerWithNoToolCallsEndsTheRun(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{answered(text("42"))}}
	got, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(), Messages: ask("what?"), Model: m})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Output != "42" {
		t.Errorf("Output = %q, want 42", got.Output)
	}
	if got.Steps != 1 {
		t.Errorf("Steps = %d, want 1", got.Steps)
	}
}

func TestAToolResultIsAppendedAndTheLoopContinues(t *testing.T) {
	t.Parallel()

	var ran bool
	m := &fakeModel{turns: []turn{
		answered(calling("lookup", `{"id":1}`)),
		answered(text("done")),
	}}

	got, err := ReAct().Run(t.Context(), agent.Input{
		Spec:     spec(tool("lookup", "the answer", nil, &ran)),
		Messages: ask("look it up"),
		Model:    m,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran {
		t.Fatal("the tool was never invoked")
	}
	if got.Steps != 2 {
		t.Errorf("Steps = %d, want 2", got.Steps)
	}

	// user, assistant(tool_call), tool, assistant(text)
	if len(got.Messages) != 4 {
		t.Fatalf("history has %d messages, want 4: %+v", len(got.Messages), got.Messages)
	}
	result := got.Messages[2]
	if result.Role != agent.RoleTool {
		t.Errorf("message 2 has role %q, want tool", result.Role)
	}
	if result.ToolCallID != "call_1" {
		t.Errorf("the tool result does not name its call: %q", result.ToolCallID)
	}
	if result.Text() != "the answer" {
		t.Errorf("tool result text = %q", result.Text())
	}
}

// The three failures that must not end the run. Each is something the model can
// read and try differently; returning an error instead kills a run that had not
// failed.
func TestARecoverableToolFailureBecomesAnObservation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		tools   []agent.Tool
		call    agent.Message
		wantOut string
	}{
		{
			name:    "a tool that is not in this step",
			tools:   nil,
			call:    calling("missing", `{}`),
			wantOut: "no tool named",
		},
		{
			name:    "arguments that are not valid JSON",
			tools:   []agent.Tool{tool("lookup", "unreachable", nil, nil)},
			call:    calling("lookup", `{"id":`),
			wantOut: "not valid JSON",
		},
		{
			name:    "a tool that returned an error",
			tools:   []agent.Tool{tool("lookup", "", errors.New("upstream is down"), nil)},
			call:    calling("lookup", `{}`),
			wantOut: "upstream is down",
		},
		{
			name:    "a tool with no implementation",
			tools:   []agent.Tool{{Name: "lookup"}},
			call:    calling("lookup", `{}`),
			wantOut: "cannot be called",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := &fakeModel{turns: []turn{answered(tc.call), answered(text("recovered"))}}
			got, err := ReAct().Run(t.Context(), agent.Input{
				Spec: spec(tc.tools...), Messages: ask("try"), Model: m,
			})
			if err != nil {
				t.Fatalf("a recoverable failure ended the run: %v", err)
			}
			if got.Output != "recovered" {
				t.Errorf("Output = %q, want the run to have carried on", got.Output)
			}
			result := got.Messages[2]
			if result.Role != agent.RoleTool {
				t.Fatalf("message 2 has role %q, want a tool result", result.Role)
			}
			if !strings.Contains(result.Text(), tc.wantOut) {
				t.Errorf("the model was told %q, want it to mention %q", result.Text(), tc.wantOut)
			}
		})
	}
}

// Invalid arguments must be caught before dispatch, not by the tool. A tool
// that is handed unparsable JSON has already been given a job it cannot do.
func TestInvalidArgumentsNeverReachTheTool(t *testing.T) {
	t.Parallel()

	var ran bool
	m := &fakeModel{turns: []turn{answered(calling("lookup", `{"id":`)), answered(text("ok"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("lookup", "", nil, &ran)), Messages: ask("try"), Model: m,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran {
		t.Error("the tool was invoked with arguments that were not valid JSON")
	}
}

func TestASpecWithNoCeilingIsRefused(t *testing.T) {
	t.Parallel()

	s := spec()
	s.MaxSteps = 0
	_, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Model: &fakeModel{}})
	if !errors.Is(err, agent.ErrNoMaxSteps) {
		t.Fatalf("err = %v, want ErrNoMaxSteps", err)
	}
}

func TestNoModelClientIsRefused(t *testing.T) {
	t.Parallel()

	_, err := ReAct().Run(t.Context(), agent.Input{Spec: spec()})
	if err == nil {
		t.Fatal("Run accepted an Input with no ModelClient")
	}
}

// Hitting the ceiling returns the partial Result as well as the error. That
// Result is what somebody deciding whether to raise the limit reads.
func TestExhaustingTheCeilingReturnsWhatHappened(t *testing.T) {
	t.Parallel()

	s := spec(tool("spin", "again", nil, nil))
	s.MaxSteps = 2
	m := &fakeModel{turns: []turn{
		answered(calling("spin", `{}`)),
		answered(calling("spin", `{}`)),
	}}

	got, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: ask("go"), Model: m})
	if !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	if got.Steps != 2 {
		t.Errorf("Steps = %d, want 2 — the count is how you tell a finished run from a truncated one", got.Steps)
	}
	if len(got.Messages) == 0 {
		t.Error("the partial history was thrown away")
	}
}

// A turn cut at MaxTokens can carry a tool call whose arguments stop mid-JSON.
// Dispatching it runs something the model never finished asking for.
func TestATruncatedTurnIsNotDispatched(t *testing.T) {
	t.Parallel()

	var ran bool
	m := &fakeModel{turns: []turn{{resp: agent.Response{
		Message: calling("lookup", `{"id":1}`),
		Finish:  agent.FinishLength,
	}}}}

	got, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("lookup", "", nil, &ran)), Messages: ask("try"), Model: m,
	})
	if !errors.Is(err, agent.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if ran {
		t.Error("a tool call from a truncated turn was dispatched")
	}
	if got.Steps != 1 {
		t.Errorf("Steps = %d, want 1", got.Steps)
	}
}

// A cancelled context must surface as cancellation, not as a tool result. The
// model should never reason about our shutdown.
func TestCancellationDuringAToolEndsTheRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	stopper := agent.Tool{
		Name: "slow",
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			cancel()
			return "never seen", nil
		},
	}

	m := &fakeModel{turns: []turn{answered(calling("slow", `{}`)), answered(text("unreachable"))}}
	got, err := ReAct().Run(ctx, agent.Input{Spec: spec(stopper), Messages: ask("go"), Model: m})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	for _, msg := range got.Messages {
		if msg.Role == agent.RoleTool {
			t.Error("a tool result from a cancelled run reached the history")
		}
	}
}

// append on a caller's slice with spare capacity writes into their backing
// array. Without the clone, a brain reusing a session history across two turns
// finds the first turn's tool results already in it.
func TestTheCallersHistoryIsNotMutated(t *testing.T) {
	t.Parallel()

	msgs := make([]agent.Message, 1, 8)
	msgs[0] = ask("hello")[0]

	m := &fakeModel{turns: []turn{answered(text("hi"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(), Messages: msgs, Model: m}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("the caller's slice grew to %d", len(msgs))
	}
	if got := msgs[:cap(msgs)][1]; got.Role != "" {
		t.Errorf("the pattern wrote %q into the caller's spare capacity", got.Role)
	}
}

// Usage is totalled by the Runner at the model client, so a run's figure
// cannot miss a call whatever the pattern did or forgot to do.
func TestUsageIsSummedAcrossSteps(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		{resp: agent.Response{
			Message: calling("count", `{}`), Finish: agent.FinishToolCalls,
			Usage: agent.Usage{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 1},
		}},
		{resp: agent.Response{
			Message: text("3"), Finish: agent.FinishStop,
			Usage: agent.Usage{InputTokens: 20, OutputTokens: 6, CachedInputTokens: 2},
		}},
	}}

	got := through(t, ReAct(), spec(tool("count", "3", nil, nil)), ask("how many?"), model)

	want := agent.Usage{InputTokens: 30, OutputTokens: 10, CachedInputTokens: 3}
	if got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
}

func TestNilEventsIsNotAnError(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{answered(text("fine"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(), Messages: ask("go"), Model: m, Events: nil,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestEveryStageOfARunIsReported(t *testing.T) {
	t.Parallel()

	events := make(chan agent.Event, 32)
	m := &fakeModel{turns: []turn{answered(calling("lookup", `{}`)), answered(text("done"))}}

	if _, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("lookup", "x", nil, nil)), Messages: ask("go"), Model: m, Events: events,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)

	seen := map[string]int{}
	for e := range events {
		switch e.(type) {
		case agent.StepEvent:
			seen["step"]++
		case agent.TextEvent:
			seen["text"]++
		case agent.ToolCallEvent:
			seen["call"]++
		case agent.ToolResultEvent:
			seen["result"]++
		case agent.UsageEvent:
			seen["usage"]++
		}
	}

	for kind, want := range map[string]int{"step": 2, "call": 1, "result": 1, "usage": 2, "text": 1} {
		if seen[kind] != want {
			t.Errorf("saw %d %s events, want %d", seen[kind], kind, want)
		}
	}
}

// The streaming path must reach the same Result as the unary one — a caller
// that only wants the answer should not care which pattern ran.
func TestStreamingEmitsDeltasAndReachesTheSameResult(t *testing.T) {
	t.Parallel()

	events := make(chan agent.Event, 32)
	m := &fakeModel{
		turns:  []turn{answered(text("hello world"))},
		deltas: []string{"hello", " ", "world"},
	}

	got, err := ReActStreaming().Run(t.Context(), agent.Input{
		Spec: spec(), Messages: ask("go"), Model: m, Events: events,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(events)

	if got.Output != "hello world" {
		t.Errorf("Output = %q, want the assembled turn", got.Output)
	}

	var streamed string
	for e := range events {
		if te, ok := e.(agent.TextEvent); ok {
			streamed += te.Delta
		}
	}
	if streamed != "hello world" {
		t.Errorf("the deltas joined to %q, want them in order", streamed)
	}
}

// A stream that ends without its final event is a broken ModelClient, not a
// finished turn. Treating it as finished silently loses tool calls.
func TestAStreamWithNoFinalEventIsRefused(t *testing.T) {
	t.Parallel()

	m := &fakeModel{turns: []turn{answered(text("lost"))}, deltas: []string{"lo"}, noFinal: true}
	_, err := ReActStreaming().Run(t.Context(), agent.Input{Spec: spec(), Messages: ask("go"), Model: m})
	if !errors.Is(err, agent.ErrIncompleteStream) {
		t.Fatalf("err = %v, want ErrIncompleteStream", err)
	}
}

// Both constructors are the same pattern, so a Runner takes one or the other.
// If they ever disagree, registration silently accepts both.
func TestBothReActConstructorsAnswerTheSameName(t *testing.T) {
	t.Parallel()

	if ReAct().Name() != agent.PatternReAct {
		t.Errorf("ReAct is named %q", ReAct().Name())
	}
	if ReActStreaming().Name() != agent.PatternReAct {
		t.Errorf("ReActStreaming is named %q", ReActStreaming().Name())
	}
}

// A stream that never opened is a plain failure, and the step it happened on
// belongs in the message — a run that dies on step 4 of 5 reads very
// differently from one that never started.
func TestAStreamThatFailsToOpenNamesItsStep(t *testing.T) {
	t.Parallel()

	boom := errors.New("gateway refused the connection")
	m := &fakeModel{turns: []turn{{err: boom}}}

	_, err := ReActStreaming().Run(t.Context(), agent.Input{Spec: spec(), Messages: ask("go"), Model: m})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the client's own error", err)
	}
	if !strings.Contains(err.Error(), "step 1") {
		t.Errorf("the error does not say which step failed: %v", err)
	}
}

// A connection dropped mid-stream must not look like a finished turn, even
// though the deltas that did arrive were real.
func TestAStreamThatBreaksMidwayIsAFailure(t *testing.T) {
	t.Parallel()

	dropped := errors.New("connection reset")
	m := &fakeModel{
		turns:     []turn{answered(text("half an answer"))},
		deltas:    []string{"half an "},
		noFinal:   true,
		streamErr: dropped,
	}

	_, err := ReActStreaming().Run(t.Context(), agent.Input{Spec: spec(), Messages: ask("go"), Model: m})
	if !errors.Is(err, dropped) {
		t.Fatalf("err = %v, want the stream's own error", err)
	}
	if errors.Is(err, agent.ErrIncompleteStream) {
		t.Error("a dropped connection was reported as a broken client")
	}
}

// The unary path's client error gets the same treatment.
func TestACompleteFailureNamesItsStep(t *testing.T) {
	t.Parallel()

	boom := errors.New("429 rate limited")
	m := &fakeModel{turns: []turn{answered(calling("lookup", `{}`)), {err: boom}}}

	got, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("lookup", "x", nil, nil)), Messages: ask("go"), Model: m,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the client's error", err)
	}
	if !strings.Contains(err.Error(), "step 2") {
		t.Errorf("the error does not say which step failed: %v", err)
	}
	if len(got.Messages) == 0 {
		t.Error("the history up to the failure was thrown away")
	}
}

// through runs a pattern the way the brain does, so Result.Usage is filled in
// by the count the Runner takes at the model client. A pattern run directly
// reports none: accounting is deliberately not a pattern's job, because a
// pattern written outside this module would forget it.
func through(t *testing.T, p agent.Pattern, s agent.Spec, msgs []agent.Message, model agent.ModelClient) agent.Result {
	t.Helper()

	r, err := agent.New(func(agent.Endpoint) (agent.ModelClient, error) { return model, nil }, p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.Pattern = p.Name()

	result, err := r.Run(t.Context(), s, msgs, agent.Endpoint{BaseURL: "http://gateway"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

// history is a conversation that already happened: a question, the answer, a
// tool the assistant called and what it returned. The brain loads this from
// Postgres and hands it over; nothing in the SDK stores it.
func history() []agent.Message {
	return []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "how many issues in cli?"}}},
		calling("count", `{"repo":"cli"}`),
		{Role: agent.RoleTool, ToolCallID: "call_1", Content: []agent.Content{{Type: agent.ContentText, Text: "cli has 0 open issues"}}},
		{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "none"}}},
		{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "and brain?"}}},
	}
}

// assertCarries checks that a request opens with the conversation it was given,
// in order. A pattern that dropped or reordered it would answer a question
// nobody asked, and every assertion about the new turn would still pass.
func assertCarries(t *testing.T, req agent.Request, prior []agent.Message) {
	t.Helper()

	if len(req.Messages) < len(prior) {
		t.Fatalf("the request carried %d messages, want at least the %d it was given",
			len(req.Messages), len(prior))
	}
	for i, want := range prior {
		got := req.Messages[i]
		if got.Role != want.Role || got.Text() != want.Text() {
			t.Errorf("message %d is %s %q, want %s %q", i, got.Role, got.Text(), want.Role, want.Text())
		}
		if got.ToolCallID != want.ToolCallID {
			t.Errorf("message %d answers call %q, want %q", i, got.ToolCallID, want.ToolCallID)
		}
		if len(got.ToolCalls) != len(want.ToolCalls) {
			t.Errorf("message %d carries %d tool calls, want %d", i, len(got.ToolCalls), len(want.ToolCalls))
		}
	}
}

func TestReActCarriesTheConversationItWasGiven(t *testing.T) {
	t.Parallel()

	prior := history()
	model := &fakeModel{turns: []turn{
		answered(calling("count", `{"repo":"brain"}`)),
		answered(text("3")),
	}}

	result, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool("count", "brain has 3 open issues", nil, nil)), Messages: prior, Model: model,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Every turn, not only the first: a pattern that rebuilt the transcript
	// from its own state would lose the history on the second call.
	for i, call := range model.calls {
		t.Run(fmt.Sprintf("call %d", i), func(t *testing.T) {
			assertCarries(t, call, prior)
		})
	}

	// And what comes back is the whole conversation, for the brain to store.
	if len(result.Messages) != len(prior)+3 {
		t.Errorf("Result.Messages holds %d, want the %d it was given plus the three this run added",
			len(result.Messages), len(prior))
	}
	if len(prior) != 5 {
		t.Error("the caller's slice was modified")
	}
}
