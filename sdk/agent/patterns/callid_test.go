package patterns

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// recorder is a tool that keeps the call id each invocation saw.
func recorder(name string, seen *[]string) agent.Tool {
	return agent.Tool{
		Name: name, Schema: json.RawMessage(`{"type":"object"}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			*seen = append(*seen, agent.ToolCallID(ctx))
			return "ok", nil
		},
	}
}

// Two calls in one response: each tool sees its own id, not the first or the
// last. A tool that keys durable work on the id would otherwise give the
// second call the first one's result.
func TestEveryToolSeesItsOwnCallID(t *testing.T) {
	t.Parallel()

	var seen []string
	model := &fakeModel{turns: []turn{
		answered(agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{
			{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)},
			{ID: "c2", Name: "probe", Arguments: json.RawMessage(`{}`)},
		}}),
		answered(text("done")),
	}}

	if _, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(recorder("probe", &seen)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(seen, []string{"c1", "c2"}) {
		t.Errorf("the tools saw %q, want each its own: [c1 c2]", seen)
	}
}

// ReWOO names its calls itself; the tool sees those names.
func TestAReWOOStepSeesItsStepID(t *testing.T) {
	t.Parallel()

	var seen []string
	model := &fakeModel{turns: []turn{
		submitted(`[{"tool":"probe","args":{},"why":"a"},{"tool":"probe","args":{},"why":"b"}]`),
		answered(text("done")),
	}}

	if _, err := ReWOO().Run(t.Context(), agent.Input{
		Spec: rewooSpec(recorder("probe", &seen)), Messages: ask("go"), Model: model,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(seen, []string{"#E1", "#E2"}) {
		t.Errorf("the steps saw %q, want [#E1 #E2]", seen)
	}
}
