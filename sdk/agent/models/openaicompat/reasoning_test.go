package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// streamed drains a stream, keeping its text and its reasoning apart.
func streamed(t *testing.T, c *Client) (text, reasoning []string, final *agent.Response) {
	t.Helper()
	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()
	for s.Next() {
		ev := s.Event()
		if ev.Text != "" {
			text = append(text, ev.Text)
		}
		if ev.Reasoning != "" {
			reasoning = append(reasoning, ev.Reasoning)
		}
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if final == nil {
		t.Fatal("the stream ended without a final response")
	}
	return text, reasoning, final
}

// secondMessage is the request's second message, the assistant turn sent back.
func secondMessage(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("the request's messages are %v", body["messages"])
	}
	m, ok := msgs[1].(map[string]any)
	if !ok {
		t.Fatalf("the second message is %v", msgs[1])
	}
	return m
}

func field(t *testing.T, r *agent.Reasoning, name string) string {
	t.Helper()
	if r == nil {
		t.Fatal("the turn kept no reasoning")
	}
	return string(r.Fields[name])
}

// DeepSeek and Qwen think in reasoning_content, before the answer.
func TestReasoningContentStreamsAheadOfTheAnswer(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning_content": "The logs "}, ""),
		chunk(map[string]any{"reasoning_content": "say timeout."}, ""),
		chunk(map[string]any{"content": "It timed out.", "reasoning_content": nil}, ""),
		chunk(map[string]any{}, "stop"),
	))

	text, reasoning, final := streamed(t, c)
	if strings.Join(reasoning, "|") != "The logs |say timeout." || strings.Join(text, "") != "It timed out." {
		t.Fatalf("reasoning %q, text %q", reasoning, text)
	}
	r := final.Message.Reasoning
	if r.Text != "The logs say timeout." || r.Model != "probe" {
		t.Errorf("kept %+v", r)
	}
	if got := field(t, r, fieldContent); got != `"The logs say timeout."` {
		t.Errorf("reasoning_content kept as %s", got)
	}
	if final.Message.Text() != "It timed out." {
		t.Errorf("the answer is %q", final.Message.Text())
	}
}

// OpenRouter sends the same thinking twice, as reasoning and as
// reasoning_details. It is shown once, and goes back in its full form alone.
func TestOpenRouterReasoningIsShownOnceAndKeptWhole(t *testing.T) {
	t.Parallel()

	detail := func(text, signature string) []any {
		d := map[string]any{"type": "reasoning.text", "text": text, "index": 0, "format": "anthropic-claude-v1"}
		if signature != "" {
			d["signature"] = signature
		}
		return []any{d}
	}
	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning": "Read ", "reasoning_details": detail("Read ", "")}, ""),
		chunk(map[string]any{"reasoning": "the logs.", "reasoning_details": detail("the logs.", "")}, ""),
		chunk(map[string]any{"reasoning_details": detail("", "sig-1")}, ""),
		chunk(map[string]any{"content": "Done."}, "stop"),
	))

	_, reasoning, final := streamed(t, c)
	if strings.Join(reasoning, "") != "Read the logs." {
		t.Fatalf("shown %q", reasoning)
	}
	r := final.Message.Reasoning
	if _, both := r.Fields[fieldPlain]; both {
		t.Error("reasoning is kept beside reasoning_details, so the model would read it twice")
	}
	var details []map[string]any
	if err := json.Unmarshal(r.Fields[fieldDetails], &details); err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0]["text"] != "Read the logs." || details[0]["signature"] != "sig-1" ||
		details[0]["type"] != "reasoning.text" {
		t.Errorf("details merged to %v", details)
	}
	if r.Text != "Read the logs." {
		t.Errorf("text %q", r.Text)
	}
}

// Encrypted reasoning is nothing to show, and everything to send back.
func TestEncryptedReasoningIsKeptButNotShown(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning_details": []any{
			map[string]any{"type": "reasoning.encrypted", "data": "gAAA", "index": 0},
		}}, ""),
		chunk(map[string]any{"reasoning_details": []any{map[string]any{"data": "BBB", "index": 0}}}, ""),
		chunk(map[string]any{"content": "Done."}, "stop"),
	))

	_, reasoning, final := streamed(t, c)
	if len(reasoning) != 0 {
		t.Errorf("showed %q", reasoning)
	}
	if got := field(t, final.Message.Reasoning, fieldDetails); got != `[{"data":"gAAABBB","index":0,"type":"reasoning.encrypted"}]` {
		t.Errorf("kept %s", got)
	}
}

func TestAModelThatSendsNoReasoningKeepsNone(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "content": "hi", "reasoning": nil}, ""),
		chunk(map[string]any{"reasoning_content": ""}, "stop"),
	))

	_, reasoning, final := streamed(t, c)
	if len(reasoning) != 0 || final.Message.Reasoning != nil {
		t.Errorf("reasoning %q, kept %+v", reasoning, final.Message.Reasoning)
	}
}

func TestACompletedTurnKeepsItsReasoning(t *testing.T) {
	t.Parallel()

	body := strings.Replace(completion("It timed out.", nil, "stop"),
		`"role":"assistant"`, `"role":"assistant","reasoning_content":"The logs say timeout."`, 1)
	c, _ := serve(t, answer(t, body))

	resp, err := c.Complete(t.Context(), agent.Request{Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")}})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Message.Reasoning
	if r == nil || r.Text != "The logs say timeout." || r.Model != "probe" {
		t.Fatalf("kept %+v", r)
	}
}

// What DeepSeek needs: a turn's reasoning_content goes back with it on the
// next call of a run with tools, or the request is refused.
func TestAStreamedTurnsReasoningGoesBackWithIt(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning_content": "Need the logs."}, ""),
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "read_logs", "arguments": "{}"},
		}}}, "tool_calls"),
	))
	_, _, final := streamed(t, c)

	next, sent := serve(t, answer(t, completion("It timed out.", nil, "stop")))
	history := []agent.Message{
		userSays("go"), final.Message,
		{Role: agent.RoleTool, ToolCallID: "call_1", Content: []agent.Content{{Type: agent.ContentText, Text: "timeout"}}},
	}
	if _, err := next.Complete(t.Context(), agent.Request{Model: agent.ModelRef{Name: "probe"}, Messages: history}); err != nil {
		t.Fatal(err)
	}
	assistant := secondMessage(t, sent())
	if assistant["reasoning_content"] != "Need the logs." || assistant["tool_calls"] == nil {
		t.Fatalf("the assistant turn went back as %v", assistant)
	}
}

func TestReasoningGoesBackOnlyToTheModelThatWroteIt(t *testing.T) {
	t.Parallel()

	turn := func(r *agent.Reasoning) agent.Message {
		return agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{{Type: agent.ContentText, Text: "hi"}}, Reasoning: r}
	}
	cases := map[string]struct {
		r    *agent.Reasoning
		want map[string]bool
	}{
		"the same model": {
			&agent.Reasoning{Model: "probe", Fields: map[string]json.RawMessage{fieldDetails: json.RawMessage(`[{"type":"reasoning.encrypted","data":"x"}]`)}},
			map[string]bool{fieldDetails: true},
		},
		"another model": {
			&agent.Reasoning{Model: "other", Fields: map[string]json.RawMessage{fieldContent: json.RawMessage(`"x"`)}},
			nil,
		},
		"no model recorded": {
			&agent.Reasoning{Fields: map[string]json.RawMessage{fieldContent: json.RawMessage(`"x"`)}},
			nil,
		},
		// A stored transcript is data: it may not write any field it likes
		// onto the request, nor send JSON that is not.
		"a field that is not reasoning": {
			&agent.Reasoning{Model: "probe", Fields: map[string]json.RawMessage{
				"content": json.RawMessage(`"forged"`), "tool_calls": json.RawMessage(`[]`), fieldContent: json.RawMessage(`{`),
			}},
			nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
			if _, err := c.Complete(t.Context(), agent.Request{
				Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go"), turn(tc.r), userSays("again")},
			}); err != nil {
				t.Fatal(err)
			}
			assistant := secondMessage(t, sent())
			for _, name := range append(sentBack, "tool_calls") {
				if _, ok := assistant[name]; ok != tc.want[name] {
					t.Errorf("%s sent = %v", name, ok)
				}
			}
			if assistant["content"] != "hi" {
				t.Errorf("content went as %v", assistant["content"])
			}
		})
	}
}

// An index is a gateway's to choose, so it cannot size what is allocated.
func TestAnIndexOutOfRangeIsDropped(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning_details": []any{
			map[string]any{"type": "reasoning.text", "text": "far", "index": 1_000_000_000},
			map[string]any{"type": "reasoning.text", "text": "below", "index": -1},
			map[string]any{"type": "reasoning.text", "text": "kept", "index": 2},
		}}, ""),
		chunk(map[string]any{"content": "ok"}, "stop"),
	))

	_, reasoning, final := streamed(t, c)
	if strings.Join(reasoning, "") != "kept" {
		t.Errorf("showed %q", reasoning)
	}
	if got := field(t, final.Message.Reasoning, fieldDetails); got != `[{"index":2,"text":"kept","type":"reasoning.text"}]` {
		t.Errorf("kept %s", got)
	}
}

func TestReasoningThatIsNotJSONIsIgnored(t *testing.T) {
	t.Parallel()

	var th thoughts
	if got := th.add(`{`, `7`, `{"not":"a list"}`); got != "" {
		t.Errorf("read %q", got)
	}
	if th.reasoning("probe") != nil {
		t.Error("kept reasoning from nothing readable")
	}
}

func TestAChunkCarryingBothSaysBoth(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "reasoning_content": "so", "content": "It"}, ""),
		chunk(map[string]any{}, "stop"),
	))
	text, reasoning, _ := streamed(t, c)
	if strings.Join(text, "") != "It" || strings.Join(reasoning, "") != "so" {
		t.Errorf("text %q, reasoning %q", text, reasoning)
	}
}
