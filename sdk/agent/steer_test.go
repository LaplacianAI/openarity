package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

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

func TestASteerBecomesAUserMessageAndLeavesTheToolResultAlone(t *testing.T) {
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
	if len(sent) != len(msgs)+1 {
		t.Fatalf("sent %d messages, want %d — the steer is a message of its own",
			len(sent), len(msgs)+1)
	}
	if sent[2].Role != RoleTool || sent[2].ToolCallID != "1" {
		t.Fatalf("the message after the tool call is %q, want the tool result", sent[2].Role)
	}
	if got := text(sent[2]); got != "12 files" {
		t.Errorf("the tool result was rewritten:\n%s", got)
	}
	if sent[3].Role != RoleUser {
		t.Errorf("the steer arrived as %q, want %q", sent[3].Role, RoleUser)
	}
	if got := text(sent[3]); !strings.Contains(got, "the bug is in vault.go") {
		t.Errorf("the appended message does not carry the steer:\n%s", got)
	}
}

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

	var got []string
	for _, m := range inner.requests()[0].Messages {
		got = append(got, text(m))
	}
	joined := strings.Join(got, "\n")
	first, second := strings.Index(joined, "first"), strings.Index(joined, "second")
	if first < 0 || second < 0 {
		t.Fatalf("a steer was dropped:\n%s", joined)
	}
	if first > second {
		t.Errorf("the steers came out in the wrong order:\n%s", joined)
	}
	if len(got) != 2 {
		t.Errorf("two things the user said arrived as %d messages, want 2", len(got))
	}
}

func TestASteerStaysOnEveryLaterRequest(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	if err := box.add("stop reading tests"); err != nil {
		t.Fatalf("add() = %v", err)
	}
	for range 3 {
		if _, err := client.Complete(t.Context(), Request{}); err != nil {
			t.Fatalf("Complete() = %v", err)
		}
	}

	for i, req := range inner.requests() {
		if len(req.Messages) != 1 {
			t.Fatalf("request %d carried %d messages, want 1", i, len(req.Messages))
		}
		if got := text(req.Messages[0]); !strings.Contains(got, "stop reading tests") {
			t.Errorf("request %d lost the steer:\n%s", i, got)
		}
		if got := strings.Count(text(req.Messages[0]), "stop reading tests"); got != 1 {
			t.Errorf("request %d repeated the steer %d times", i, got)
		}
	}
}

func TestASteerIsAnnouncedOnlyWhenItIsNew(t *testing.T) {
	var seen []string
	box := &steerBox{}
	client, _ := steering(box, func(e Event) {
		if s, ok := e.(SteerEvent); ok {
			seen = append(seen, s.Text)
		}
	})

	if err := box.add("once"); err != nil {
		t.Fatalf("add() = %v", err)
	}
	for range 3 {
		if _, err := client.Complete(t.Context(), Request{}); err != nil {
			t.Fatalf("Complete() = %v", err)
		}
	}

	if len(seen) != 1 {
		t.Errorf("announced %d times, want 1: %q", len(seen), seen)
	}
}

func TestTheTranscriptRecordsWhatTheUserSaid(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "why?"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "grep"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "12 files"}}},
		{Role: RoleAssistant, Content: []Content{{Type: ContentText, Text: "it is vault.go"}}},
	}

	got := recordSteers(msgs, []steer{{text: "stop reading tests", at: 3}})
	if len(got) != len(msgs)+1 {
		t.Fatalf("recorded %d messages, want %d", len(got), len(msgs)+1)
	}
	if got[3].Role != RoleUser {
		t.Errorf("the steer sits at index 3 as %q, want %q", got[3].Role, RoleUser)
	}
	if !strings.Contains(text(got[3]), "stop reading tests") {
		t.Errorf("the recorded message does not carry the steer:\n%s", text(got[3]))
	}
	if text(got[4]) != "it is vault.go" {
		t.Errorf("the answer no longer ends the transcript: %q", text(got[4]))
	}
}

func TestASteerStaysWhereItArrivedRatherThanFollowingTheEnd(t *testing.T) {
	box := &steerBox{}
	client, inner := steering(box, nil)

	msgs := []Message{
		{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "why?"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "grep"}}},
		{Role: RoleTool, ToolCallID: "1", Content: []Content{{Type: ContentText, Text: "12 files"}}},
	}
	if err := box.add("look in vault.go"); err != nil {
		t.Fatalf("add() = %v", err)
	}
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	msgs = append(msgs,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "2", Name: "grep"}}},
		Message{Role: RoleTool, ToolCallID: "2", Content: []Content{{Type: ContentText, Text: "vault.go:88"}}})
	if _, err := client.Complete(t.Context(), Request{Messages: msgs}); err != nil {
		t.Fatalf("Complete() = %v", err)
	}

	sent := inner.requests()[1].Messages
	if got := sent[len(sent)-1]; got.Role != RoleTool {
		t.Errorf("the request ends with a %q message, want the tool result — a steer that is\n"+
			"always last reads as a fresh instruction every turn and the model never concludes",
			got.Role)
	}
	if got := text(sent[3]); !strings.Contains(got, "look in vault.go") {
		t.Errorf("the steer did not stay at the index it arrived at, index 3 holds:\n%s", got)
	}
}

func TestNothingIsRecordedWhenNobodySteered(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: []Content{{Type: ContentText, Text: "why?"}}}}
	if got := recordSteers(msgs, nil); len(got) != 1 {
		t.Errorf("recordSteers() added %d messages to a run nobody steered", len(got)-1)
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
	msgs = append(msgs, Message{
		Role:    RoleAssistant,
		Content: []Content{{Type: ContentText, Text: "finished"}},
	})
	return Result{Output: "finished", Messages: msgs}, nil
}

func TestTheRunnerPutsTheSteerIntoTheTranscriptItHandsBack(t *testing.T) {
	inner := &recordingClient{}
	runner, err := New(func(Endpoint) (ModelClient, error) { return inner, nil }, &obliviousPattern{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	run := runner.Start(t.Context(), Spec{Pattern: "oblivious", MaxSteps: 1}, nil, Endpoint{}, nil)
	if err := run.Steer("stop reading tests"); err != nil {
		t.Fatalf("Steer() = %v", err)
	}
	result, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	var found int
	for _, m := range result.Messages {
		if strings.Contains(text(m), "stop reading tests") {
			found++
			if m.Role != RoleUser {
				t.Errorf("the transcript records the steer as %q, want %q", m.Role, RoleUser)
			}
		}
	}
	if found != 1 {
		t.Fatalf("the transcript carries the steer %d times, want 1:\n%+v", found, result.Messages)
	}
	if last := result.Messages[len(result.Messages)-1]; text(last) != "finished" {
		t.Errorf("the answer no longer ends the transcript: %q", text(last))
	}
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
