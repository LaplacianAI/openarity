package agentstream

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

func TestConvert(t *testing.T) {
	cases := []struct {
		in   agent.Event
		want stream.Event
	}{
		{agent.TextEvent{Delta: "Hel"}, stream.TextMessageChunk{Delta: "Hel"}},
		{
			agent.ToolCallEvent{ID: "c1", Name: "search", Arguments: json.RawMessage(`{"q":"x"}`)},
			stream.ToolCallChunk{ToolCallID: "c1", Name: "search", Arguments: `{"q":"x"}`},
		},
		{
			agent.ToolResultEvent{ID: "c1", Name: "search", Output: "3", Err: "slow", Duration: 1500 * time.Microsecond},
			stream.ToolCallResult{ToolCallID: "c1", Name: "search", Content: "3", Error: "slow", DurationMS: 1},
		},
		{agent.StepEvent{Step: 2}, stream.StepStarted{Step: 2}},
		{
			agent.UsageEvent{Model: "m", Usage: agent.Usage{InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3}},
			stream.Usage{Model: "m", InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3},
		},
		{
			agent.CompactedEvent{Reason: "tokens", Cleared: 1, Folded: 2, Before: 3, After: 4, Summary: "s", Err: "e"},
			stream.Compacted{Reason: "tokens", Cleared: 1, Folded: 2, Before: 3, After: 4, Summary: "s", Error: "e"},
		},
	}
	for _, c := range cases {
		got, ok := Convert(c.in)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Convert(%#v) = %#v, %v; want %#v", c.in, got, ok, c.want)
		}
	}
}

func TestASteerHasNoEventOfItsOwn(t *testing.T) {
	if got, ok := Convert(agent.SteerEvent{Text: "also check staging"}); ok {
		t.Fatalf("a steer became %#v", got)
	}
}

func TestPipeAppendsInOrderUntilClosed(t *testing.T) {
	events := make(chan agent.Event, 4)
	events <- agent.StepEvent{Step: 1}
	events <- agent.SteerEvent{Text: "skipped"}
	events <- agent.TextEvent{Delta: "done"}
	close(events)

	var log stream.Memory
	Pipe(events, "r1", &log)

	got, _ := log.Since(t.Context(), stream.Start, 0)
	if len(got) != 2 || got[0].RunID != "r1" || got[0].Event.Kind() != stream.KindStepStarted ||
		got[1].Event.Kind() != stream.KindTextMessageChunk {
		t.Fatalf("got %+v", got)
	}
}
