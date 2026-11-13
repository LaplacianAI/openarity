package patterns

import (
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// A call through call_tool is shown as the tool it stands for, while the
// transcript keeps the call_tool the provider sent, which is what it needs
// back.
func TestEventsNameTheToolACallToolCallStandsFor(t *testing.T) {
	t.Parallel()

	model := &fakeModel{turns: []turn{
		answered(calling(agent.CallToolName, `{"name":"github__close_issue","arguments":{"n":42}}`)),
		answered(text("closed")),
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var calls []agent.ToolCallEvent
	var results []agent.ToolResultEvent
	go func() {
		defer close(done)
		for e := range events {
			switch e := e.(type) {
			case agent.ToolCallEvent:
				calls = append(calls, e)
			case agent.ToolResultEvent:
				results = append(results, e)
			}
		}
	}()

	result, err := ReAct().Run(t.Context(), agent.Input{
		Spec: spec(tool(agent.CallToolName, "closed #42", nil, nil)), Messages: ask("close 42"),
		Model: model, Events: events,
	})
	close(events)
	<-done
	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 1 || calls[0].Name != "github__close_issue" || string(calls[0].Arguments) != `{"n":42}` {
		t.Fatalf("the call was shown as %+v", calls)
	}
	if len(results) != 1 || results[0].Name != "github__close_issue" || results[0].Output != "closed #42" {
		t.Fatalf("the result was shown as %+v", results)
	}
	sent := result.Messages[1].ToolCalls
	if len(sent) != 1 || sent[0].Name != agent.CallToolName {
		t.Fatalf("the transcript holds %+v; it must keep the call as the model made it", sent)
	}
}
