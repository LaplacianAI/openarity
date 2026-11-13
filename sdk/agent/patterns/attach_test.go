package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func image(ref string) agent.Content {
	return agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Name: ref + ".png", Ref: ref}}
}

// attaching is a tool that answers with text and hands the model files.
func attaching(name, out string, files ...agent.Content) agent.Tool {
	return agent.Tool{
		Name:   name,
		Schema: json.RawMessage(`{"type":"object"}`),
		Result: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			return agent.ToolResult{Output: out, Attach: files}, nil
		},
	}
}

func twoCalls() agent.Message {
	return agent.Message{
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{
			{ID: "c1", Name: "read_attachment", Arguments: json.RawMessage(`{}`)},
			{ID: "c2", Name: "plain", Arguments: json.RawMessage(`{}`)},
		},
	}
}

// Each call is answered by its tool message first, as providers require; the
// files follow in one user message, named by the call that attached them.
func TestAToolsFilesFollowTheTurnsResults(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(twoCalls()), answered(text("It is a crash."))}}
	s := spec(attaching("read_attachment", "screen.png, 300 KB", image("a2")), tool("plain", "ok", nil, nil))
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: s, Messages: ask("look"), Model: model}); err != nil {
		t.Fatal(err)
	}

	msgs := model.calls[1].Messages
	tail := msgs[len(msgs)-3:]
	if tail[0].Role != agent.RoleTool || tail[0].ToolCallID != "c1" || tail[0].Text() != "screen.png, 300 KB" {
		t.Fatalf("the first result is %+v", tail[0])
	}
	if tail[1].Role != agent.RoleTool || tail[1].ToolCallID != "c2" {
		t.Fatalf("the second result is %+v", tail[1])
	}
	files := tail[2]
	if files.Role != agent.RoleUser || len(files.Content) != 2 {
		t.Fatalf("the files went as %+v", files)
	}
	if files.Content[0].Text != "Attached by read_attachment for call c1:" || files.Content[1].Blob.Ref != "a2" {
		t.Errorf("the files read %+v", files.Content)
	}
}

func TestATurnWithNoFilesAddsNoMessage(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{answered(calling("plain", `{}`)), answered(text("done"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(tool("plain", "ok", nil, nil)), Messages: ask("go"), Model: model}); err != nil {
		t.Fatal(err)
	}
	if last := model.calls[1].Messages[len(model.calls[1].Messages)-1]; last.Role != agent.RoleTool {
		t.Errorf("the turn ended with %+v", last)
	}
}

// A tool with both is called through Result, which is the richer of the two.
func TestResultIsPreferredToInvoke(t *testing.T) {
	t.Parallel()

	both := attaching("read_attachment", "from Result")
	both.Invoke = func(context.Context, json.RawMessage) (string, error) { return "from Invoke", nil }
	model := &fakeModel{turns: []turn{answered(calling("read_attachment", `{}`)), answered(text("done"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(both), Messages: ask("go"), Model: model}); err != nil {
		t.Fatal(err)
	}
	msgs := model.calls[1].Messages
	if got := msgs[len(msgs)-1].Text(); got != "from Result" {
		t.Errorf("the tool answered %q", got)
	}
}

// A tool that failed attaches nothing, whatever it returned beside the error.
func TestAFailedToolAttachesNothing(t *testing.T) {
	t.Parallel()

	failing := agent.Tool{
		Name:   "read_attachment",
		Schema: json.RawMessage(`{"type":"object"}`),
		Result: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			return agent.ToolResult{Attach: []agent.Content{image("a2")}}, errors.New("not in this session")
		},
	}
	model := &fakeModel{turns: []turn{answered(calling("read_attachment", `{}`)), answered(text("done"))}}
	if _, err := ReAct().Run(t.Context(), agent.Input{Spec: spec(failing), Messages: ask("go"), Model: model}); err != nil {
		t.Fatal(err)
	}
	msgs := model.calls[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != agent.RoleTool || last.Text() != "not in this session" {
		t.Errorf("the turn ended with %+v", last)
	}
}

func TestAToolWithNeitherCannotBeCalled(t *testing.T) {
	t.Parallel()

	empty := agent.Tool{Name: "nothing", Schema: json.RawMessage(`{"type":"object"}`)}
	if _, callable, _ := empty.Call(t.Context(), nil); callable {
		t.Error("a tool with no Invoke and no Result was called")
	}
}

// ReWOO's solver reads what the plan's steps attached, after the evidence.
func TestAReWOOStepsFilesReachTheSolver(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"read_attachment","args":{},"why":"see it"}]`),
		answered(text("It is a crash.")),
	}}
	s := rewooSpec(attaching("read_attachment", "screen.png", image("a2")))
	if _, err := ReWOO().Run(t.Context(), agent.Input{Spec: s, Messages: ask("look"), Model: model}); err != nil {
		t.Fatal(err)
	}
	msgs := model.calls[1].Messages
	evidence, files := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if evidence.Role != agent.RoleTool {
		t.Fatalf("the evidence is %+v", evidence)
	}
	if files.Role != agent.RoleUser || files.Content[0].Text != "Attached by read_attachment for call #E1:" ||
		files.Content[1].Blob.Ref != "a2" {
		t.Fatalf("the files went as %+v", files)
	}
}
