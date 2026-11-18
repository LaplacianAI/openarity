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

// waiting is a tool that suspends the run until it is let through, and
// counts every time it was asked, with the call id it was asked with.
type waiting struct {
	through bool
	asked   []string
}

func (w *waiting) tool(name string) agent.Tool {
	return agent.Tool{
		Name:   name,
		Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			w.asked = append(w.asked, agent.ToolCallID(ctx))
			if !w.through {
				return "", agent.Suspend("waiting for a person")
			}
			return "sent", nil
		},
	}
}

func threeCalls() agent.Message {
	return agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
		{ID: "c1", Name: "read_attachment", Arguments: json.RawMessage(`{}`)},
		{ID: "c2", Name: "send", Arguments: json.RawMessage(`{}`)},
		{ID: "c3", Name: "post", Arguments: json.RawMessage(`{}`)},
	}}
}

// suspended runs the first half: three calls, the second of which suspends.
func suspended(t *testing.T, gate *waiting, posted *bool) agent.Result {
	t.Helper()

	model := &fakeModel{turns: []turn{answered(threeCalls())}}
	s := spec(attaching("read_attachment", "screen.png", image("a2")), gate.tool("send"), tool("post", "posted", nil, posted))
	result, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: ask("send it"), Model: model})
	if !errors.Is(err, agent.ErrSuspended) {
		t.Fatalf("err = %v, want one that matches ErrSuspended", err)
	}
	return result
}

// A tool suspends the second of three calls: the first's result and its
// files are kept, the third is not made, and nothing of the suspension is
// written as a result for the model to read.
func TestASuspendedCallEndsTheRunWithTheTurnOpen(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	var posted bool
	result := suspended(t, gate, &posted)

	if posted {
		t.Error("the call after the suspended one was made")
	}
	if result.Steps != 1 {
		t.Errorf("Steps = %d, want 1", result.Steps)
	}
	msgs := result.Messages
	tail := msgs[len(msgs)-3:]
	if tail[0].Role != agent.RoleAssistant || len(tail[0].ToolCalls) != 3 {
		t.Fatalf("the turn is %+v", tail[0])
	}
	if tail[1].Role != agent.RoleTool || tail[1].ToolCallID != "c1" {
		t.Errorf("the first result is %+v", tail[1])
	}
	if !tail[2].Attached || tail[2].Role != agent.RoleUser || tail[2].Content[1].Blob.Ref != "a2" {
		t.Errorf("the first call's files are %+v", tail[2])
	}
	for _, m := range msgs {
		if m.Role == agent.RoleTool && (m.ToolCallID == "c2" || strings.Contains(m.Text(), "suspended")) {
			t.Errorf("the suspension was written as a result: %+v", m)
		}
	}
}

// Running the saved transcript again finishes the turn before the model is
// asked anything: the open calls in order, with the call ids they had, their
// results before the turn's files, and one model call after.
func TestAResumedRunFinishesItsTurnFirst(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	var posted bool
	saved := suspended(t, gate, &posted).Messages

	gate.through = true
	model := &fakeModel{turns: []turn{answered(text("Sent and posted."))}}
	s := spec(attaching("read_attachment", "screen.png", image("a2")), gate.tool("send"), tool("post", "posted", nil, &posted))
	result, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: saved, Model: model})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if !slices.Equal(gate.asked, []string{"c2", "c2"}) || !posted {
		t.Errorf("asked %v, posted %v; want c2 twice and the post made", gate.asked, posted)
	}
	if len(model.calls) != 1 {
		t.Fatalf("the model was called %d times, want once, after the turn", len(model.calls))
	}
	if result.Steps != 1 {
		t.Errorf("Steps = %d, want 1: finishing the turn is not a step", result.Steps)
	}

	sent := model.calls[0].Messages
	tail := sent[len(sent)-5:]
	ids := []string{tail[1].ToolCallID, tail[2].ToolCallID, tail[3].ToolCallID}
	if tail[0].Role != agent.RoleAssistant || !slices.Equal(ids, []string{"c1", "c2", "c3"}) {
		t.Fatalf("the turn went as %+v", tail)
	}
	if !tail[4].Attached || tail[4].Role != agent.RoleUser {
		t.Errorf("the turn ends with %+v, want its files", tail[4])
	}
	if !slices.Equal(saved[len(saved)-1].Content, tail[4].Content) {
		t.Errorf("the files changed on resume: %+v", tail[4].Content)
	}
}

// Files attached by a call made on resume join the turn's files message,
// rather than starting a second one.
func TestFilesAttachedOnResumeJoinTheTurnsFiles(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	saved := suspended(t, gate, nil).Messages

	gate.through = true
	model := &fakeModel{turns: []turn{answered(text("done"))}}
	s := spec(gate.tool("send"), attaching("post", "posted", image("b7")))
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: saved, Model: model}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	sent := model.calls[0].Messages
	files := sent[len(sent)-1]
	var refs []string
	for _, c := range files.Content {
		if c.Blob != nil {
			refs = append(refs, c.Blob.Ref)
		}
	}
	if !files.Attached || !slices.Equal(refs, []string{"a2", "b7"}) {
		t.Errorf("the turn's files are %v in %+v, want a2 then b7 in one message", refs, files)
	}
	if n := countAttached(sent); n != 1 {
		t.Errorf("%d files messages, want 1", n)
	}
}

func countAttached(msgs []agent.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Attached {
			n++
		}
	}
	return n
}

// Resumed while it still has to wait, the run suspends again in the same
// shape, and the model is not called.
func TestAResumeThatStillWaitsSuspendsAgain(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	var posted bool
	saved := suspended(t, gate, &posted).Messages

	model := &fakeModel{}
	s := spec(gate.tool("send"), tool("post", "posted", nil, &posted))
	result, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: saved, Model: model})
	if !errors.Is(err, agent.ErrSuspended) {
		t.Fatalf("err = %v, want ErrSuspended", err)
	}
	if len(model.calls) != 0 || posted {
		t.Errorf("model called %d times, posted %v; want neither", len(model.calls), posted)
	}
	if len(result.Messages) != len(saved) || countAttached(result.Messages) != 1 {
		t.Errorf("the transcript became %d messages with %d files messages, want it as it was",
			len(result.Messages), countAttached(result.Messages))
	}
}

// A run cancelled while a tool suspends it ends as cancelled: the person who
// stopped it does not want it parked.
func TestACancelledRunIsNotSuspended(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	stopping := agent.Tool{
		Name: "send", Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			cancel()
			return "", agent.Suspend("waiting")
		},
	}
	model := &fakeModel{turns: []turn{answered(calling("send", `{}`))}}
	_, err := ReAct().Run(ctx, agent.Input{Spec: spec(stopping), Messages: ask("go"), Model: model})
	if !errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrSuspended) {
		t.Errorf("err = %v, want cancelled and not suspended", err)
	}
}

// A suspension through call_tool, the way a deferred tool is reached, is
// still a suspension.
func TestASuspensionPassesThroughAWrapper(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	inner := gate.tool("send")
	wrapper := agent.Tool{
		Name: "call_tool", Schema: json.RawMessage(`{"type":"object"}`),
		Result: func(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
			r, _, err := inner.Call(ctx, args)
			return r, err
		},
	}
	model := &fakeModel{turns: []turn{answered(calling("call_tool", `{}`))}}
	_, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(wrapper), Messages: ask("go"), Model: model})
	if !errors.Is(err, agent.ErrSuspended) {
		t.Errorf("err = %v, want ErrSuspended", err)
	}
}

func TestTheCallersTranscriptIsUnchangedByAResume(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	saved := suspended(t, gate, nil).Messages
	// Spare capacity is what lets an append write into the caller's array.
	roomy := make([]agent.Message, len(saved), len(saved)+8)
	copy(roomy, saved)
	before := slices.Clone(roomy)
	files := slices.Clone(roomy[len(roomy)-1].Content)

	gate.through = true
	model := &fakeModel{turns: []turn{answered(text("done"))}}
	s := spec(gate.tool("send"), attaching("post", "posted", image("b7")))
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: roomy, Model: model}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(roomy) != len(before) || !slices.Equal(roomy[len(roomy)-1].Content, files) {
		t.Error("the resume changed the caller's transcript")
	}
	if extra := roomy[:cap(roomy)][len(roomy)]; extra.Role != "" {
		t.Errorf("the resume wrote %+v into the caller's spare capacity", extra)
	}
}

// A transcript whose turn is answered in full is not open: the model is next,
// with no call made again.
func TestAnAnsweredTurnIsNotMadeAgain(t *testing.T) {
	t.Parallel()

	var ran bool
	msgs := append(ask("go"), calling("send", `{}`), agent.Message{
		Role: agent.RoleTool, ToolCallID: "call_1",
		Content: []agent.Content{{Type: agent.ContentText, Text: "sent"}},
	})
	model := &fakeModel{turns: []turn{answered(text("done"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(tool("send", "sent", nil, &ran)), Messages: msgs, Model: model}); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("an answered call was made again")
	}
}

// ReWOO plans every call before making one and keeps nothing between them,
// so a step that suspends ends the run, with an error a caller cannot take
// for a run to park.
func TestReWOORefusesToWait(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	model := &fakeModel{turns: []turn{submitted(`[{"tool":"send","args":{},"why":"a"}]`)}}
	_, err := ReWOO().Run(t.Context(), agent.Input{Spec: rewooSpec(gate.tool("send")), Messages: ask("go"), Model: model})
	if !errors.Is(err, ErrCannotWait) || errors.Is(err, agent.ErrSuspended) {
		t.Errorf("err = %v, want ErrCannotWait and not ErrSuspended", err)
	}
}

// Plan resumed after a suspension does not plan again: its plan is already
// in the transcript.
func TestPlanDoesNotPlanAgainOnResume(t *testing.T) {
	t.Parallel()

	gate := &waiting{}
	model := &fakeModel{turns: []turn{
		answered(text("1. Send it.")),
		answered(calling("send", `{}`)),
	}}
	s := spec(gate.tool("send"))
	s.Pattern = agent.PatternPlan
	result, err := Plan().Run(t.Context(), agent.Input{Spec: s, Messages: ask("send it"), Model: model})
	if !errors.Is(err, agent.ErrSuspended) {
		t.Fatalf("err = %v, want ErrSuspended", err)
	}

	gate.through = true
	model = &fakeModel{turns: []turn{answered(text("Sent."))}}
	result, err = Plan().Run(t.Context(), agent.Input{Spec: s, Messages: result.Messages, Model: model})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(model.calls) != 1 || len(model.calls[0].Tools) == 0 {
		t.Errorf("resumed with %d model calls (tools in the first: %d), want one acting call",
			len(model.calls), len(model.calls[0].Tools))
	}
	if result.Output != "Sent." {
		t.Errorf("Output = %q", result.Output)
	}
}

// Only the message the pattern itself marked is a turn's files. A person's
// own message after a turn, however it got there, does not reopen the turn.
func TestOnlyTheMarkedFilesMessageIsPartOfATurn(t *testing.T) {
	t.Parallel()

	turnLeftOpen := []agent.Message{
		threeCalls(),
		{Role: agent.RoleTool, ToolCallID: "c1", Content: []agent.Content{{Type: agent.ContentText, Text: "ok"}}},
	}
	said := agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: "Attached by me:"}}}
	if _, ok := openTurn(append(slices.Clone(turnLeftOpen), said)); ok {
		t.Error("a person's message after the turn was taken for its files")
	}
	said.Attached = true
	o, ok := openTurn(append(slices.Clone(turnLeftOpen), said))
	if !ok || o.files != 2 || o.results != 2 || len(o.calls) != 2 {
		t.Errorf("openTurn = %+v, %v; want files at 2, results ending at 2, two calls left", o, ok)
	}
}

// finishTurn never writes into the transcript it was given, even with room
// to: the files it moves to the end are read from the caller's copy.
func TestFinishingATurnLeavesItsInputAlone(t *testing.T) {
	t.Parallel()

	gate := &waiting{through: true}
	saved := suspended(t, &waiting{}, nil).Messages
	roomy := make([]agent.Message, len(saved), len(saved)+8)
	copy(roomy, saved)
	before := slices.Clone(roomy)

	o, ok := openTurn(roomy)
	if !ok {
		t.Fatal("the suspended transcript is not open")
	}
	s := spec(gate.tool("send"), tool("post", "posted", nil, nil))
	out, err := finishTurn(t.Context(), agent.Input{Spec: s}, map[string]agent.Tool{"send": s.Tools[0], "post": s.Tools[1]}, roomy, o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if roomy[i].Role != before[i].Role || roomy[i].ToolCallID != before[i].ToolCallID || roomy[i].Attached != before[i].Attached {
			t.Fatalf("message %d of the input became %+v", i, roomy[i])
		}
	}
	if last := out[len(out)-1]; !last.Attached || last.Content[1].Blob.Ref != "a2" {
		t.Errorf("the turn's files were lost: %+v", last)
	}
}
