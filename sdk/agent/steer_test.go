package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// recordingClient keeps every request it was asked to make, so a test can see
// what the model would actually have been sent.
type recordingClient struct {
	mu   sync.Mutex
	sent []Request

	reply func(Request) Response
}

func (c *recordingClient) Complete(_ context.Context, req Request) (Response, error) {
	c.mu.Lock()
	c.sent = append(c.sent, req)
	c.mu.Unlock()

	if c.reply != nil {
		return c.reply(req), nil
	}
	return Response{}, nil
}

func (c *recordingClient) Stream(_ context.Context, req Request) (Stream, error) {
	c.mu.Lock()
	c.sent = append(c.sent, req)
	c.mu.Unlock()
	return nil, nil
}

func (c *recordingClient) requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.sent...)
}

func steering(box *steerBox, emit func(Event)) (*steeringClient, *recordingClient) {
	inner := &recordingClient{}
	return &steeringClient{inner: inner, box: box, emit: emit}, inner
}

func text(m Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// The rule that decides the whole design: a provider requires the message
// after a tool call to be that call's result. A steer wedged between them is a
// 400 before the model reads anything — so it goes *onto* the result instead,
// which is free-form text and always legal.
func TestASteerRidesTheToolResultRatherThanBecomingAMessage(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	if err := box.add("the bug is in vault.go"); err != nil {
		t.Fatalf("add() = %v", err)
	}

	msgs := []Message{
		{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "find the bug"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "grep"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "12 files"}}},
	}
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	sent := inner.requests()[0].Messages
	if len(sent) != len(msgs) {
		t.Fatalf("sent %d messages, want %d — a steer must not be inserted after a tool call",
			len(sent), len(msgs))
	}
	if sent[2].Role != RoleTool {
		t.Fatalf("the message after the tool call is %q, want %q", sent[2].Role, RoleTool)
	}
	if got := text(sent[2]); !strings.Contains(got, "the bug is in vault.go") {
		t.Errorf("the tool result does not carry the steer:\n%s", got)
	}
	if got := text(sent[2]); !strings.Contains(got, "12 files") {
		t.Errorf("the tool result lost its own output:\n%s", got)
	}
}

// With no tool call outstanding there is nothing to ride, and a user message
// is exactly what the steer is.
func TestASteerWithNoToolResultBecomesAUserMessage(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	if err := box.add("use the other approach"); err != nil {
		t.Fatalf("add() = %v", err)
	}

	msgs := []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "start"}}}}
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	sent := inner.requests()[0].Messages
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want 2 — the steer should be appended", len(sent))
	}
	if sent[1].Role != RoleUser {
		t.Errorf("the steer arrived as %q, want %q", sent[1].Role, RoleUser)
	}
	if got := text(sent[1]); !strings.Contains(got, "use the other approach") {
		t.Errorf("the appended message does not carry the steer:\n%s", got)
	}
}

// The slice belongs to the pattern, which keeps appending to it and hands it
// back as Result.Messages. Editing it in place would put the steer in the
// transcript as well as in the request.
func TestSteeringDoesNotTouchTheCallersMessages(t *testing.T) {
	box := &steerBox{}
	client, _ := steering(box, nil)

	if err := box.add("a steer"); err != nil {
		t.Fatalf("add() = %v", err)
	}

	result := Message{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "output"}}}
	msgs := []Message{result}

	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("the caller's slice grew to %d", len(msgs))
	}
	if got := text(msgs[0]); got != "output" {
		t.Errorf("the caller's tool result was edited to %q, want %q", got, "output")
	}
}

// A steer that changes a run's course and leaves no trace makes a transcript
// impossible to read afterwards.
func TestASteerIsAnnounced(t *testing.T) {
	var seen []Event
	box := &steerBox{}
	client, _ := steering(box, func(e Event) { seen = append(seen, e) })

	if err := box.add("look at vault.go"); err != nil {
		t.Fatalf("add() = %v", err)
	}
	if _, err := client.Complete(t.Context(), Request{}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("emitted %d events, want 1", len(seen))
	}
	ev, ok := seen[0].(SteerEvent)
	if !ok {
		t.Fatalf("emitted %T, want SteerEvent", seen[0])
	}
	if ev.Text != "look at vault.go" {
		t.Errorf("SteerEvent.Text = %q, want the steer", ev.Text)
	}
}

// Every request would otherwise pay for a clone it does not need.
func TestNoSteerLeavesTheRequestExactlyAsItWas(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	msgs := []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "hello"}}}}
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	sent := inner.requests()[0].Messages
	if len(sent) != 1 || text(sent[0]) != "hello" {
		t.Errorf("an unsteered request was altered: %+v", sent)
	}
}

// Two steers sent before the next call go out together, in the order they were
// sent, rather than one being dropped.
func TestEverySteerWaitingGoesOutOnTheNextRequest(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	for _, s := range []string{"first", "second"} {
		if err := box.add(s); err != nil {
			t.Fatalf("add(%q) = %v", s, err)
		}
	}
	if _, err := client.Complete(t.Context(), Request{}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	got := text(inner.requests()[0].Messages[0])
	first, second := strings.Index(got, "first"), strings.Index(got, "second")
	if first < 0 || second < 0 {
		t.Fatalf("a steer was dropped:\n%s", got)
	}
	if first > second {
		t.Errorf("the steers came out in the wrong order:\n%s", got)
	}
}

// Once it has gone out it must not go out again, or every subsequent request
// repeats it and the model reads it as the user saying it over and over.
func TestASteerIsDeliveredOnce(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	if err := box.add("only once"); err != nil {
		t.Fatalf("add() = %v", err)
	}
	for range 2 {
		if _, err := client.Complete(t.Context(), Request{}); err != nil {
			t.Fatalf("Complete() = %v", err)
		}
	}

	second := inner.requests()[1]
	if len(second.Messages) != 0 {
		t.Errorf("the steer was sent a second time: %+v", second.Messages)
	}
}

func TestSteeringAFinishedRunSaysSoRatherThanSwallowingIt(t *testing.T) {
	box := &steerBox{}
	box.close()

	if err := box.add("too late"); !errors.Is(err, ErrRunFinished) {
		t.Errorf("add() after close = %v, want ErrRunFinished", err)
	}
}

func TestASteerThatNeverMadeItOntoARequestIsHandedBack(t *testing.T) {
	box := &steerBox{}
	if err := box.add("never sent"); err != nil {
		t.Fatalf("add() = %v", err)
	}

	left := box.close()
	if len(left) != 1 || left[0] != "never sent" {
		t.Errorf("close() = %v, want the steer that never went out", left)
	}
}

// The point of putting this in a ModelClient rather than in a loop: a pattern
// that has never heard of steering is steered anyway. This one is not ReAct
// and shares no code with it — it calls the model once and returns what it was
// sent, which is enough to prove the steer arrived without the pattern doing
// anything.
type obliviousPattern struct{}

func (*obliviousPattern) Name() PatternName { return "oblivious" }

func (p *obliviousPattern) Run(ctx context.Context, in Input) (Result, error) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "grep"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "done"}}},
	}
	if _, err := in.Model.Complete(ctx, Request{Messages: msgs}); err != nil {
		return Result{}, err
	}
	return Result{Output: "finished"}, nil
}

func TestAPatternThatKnowsNothingOfSteeringIsStillSteered(t *testing.T) {
	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	run := runner.Start(t.Context(), Spec{Pattern: "oblivious", MaxSteps: 1}, nil, Endpoint{}, nil)
	if err := run.Steer("the bug is in vault.go"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	requests := inner.requests()
	if len(requests) != 1 {
		t.Fatalf("the pattern made %d requests, want 1", len(requests))
	}
	sent := requests[0].Messages
	if got := text(sent[len(sent)-1]); !strings.Contains(got, "the bug is in vault.go") {
		t.Errorf("the steer did not reach a pattern that does not implement steering:\n%s", got)
	}
}

func TestRunReportsASteerItCouldNotApply(t *testing.T) {
	runner, err := New(clients(), &fakePattern{name: "p"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	run := runner.Start(t.Context(), Spec{Pattern: "p"}, nil, Endpoint{}, nil)
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	// The run is over, so this can never go out.
	if err := run.Steer("too late"); !errors.Is(err, ErrRunFinished) {
		t.Errorf("Steer() after the run = %v, want ErrRunFinished", err)
	}
}

func TestWaitCanBeCalledMoreThanOnce(t *testing.T) {
	runner, err := New(clients(), &fakePattern{name: "p"})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	run := runner.Start(t.Context(), Spec{Pattern: "p"}, nil, Endpoint{}, nil)
	first, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	second, _ := run.Wait()

	if first.Output != second.Output {
		t.Errorf("a second Wait() returned %q, want %q", second.Output, first.Output)
	}
}
