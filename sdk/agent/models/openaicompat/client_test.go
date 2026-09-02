package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// serve stands up a server speaking the OpenAI wire format and returns a client
// pointed at it, plus a pointer to the last request body it received.
//
// A real server rather than a mocked SDK: the thing worth testing here is the
// JSON that leaves the process, and asserting on openai-go's structs would
// prove only that we built the structs we thought we did.
func serve(t *testing.T, handler http.HandlerFunc) (*Client, func() map[string]any) {
	t.Helper()

	// Guarded because the handler runs on the server's goroutine while the test
	// reads on its own — and one test deliberately makes twenty calls at once.
	var mu sync.Mutex
	body := map[string]any{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request: %v", err)
		}
		mu.Lock()
		clear(body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the request was not JSON: %v\n%s", err, raw)
		}
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, "sk-test"), func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(body)
	}
}

func completion(content string, toolCalls []map[string]any, finish string) string {
	msg := map[string]any{"role": "assistant", "content": content}
	if toolCalls != nil {
		msg["tool_calls"] = toolCalls
	}
	out, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl-1",
		"object":  "chat.completion",
		"model":   "probe",
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage": map[string]any{
			"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
			"prompt_tokens_details": map[string]any{"cached_tokens": 80},
		},
	})
	return string(out)
}

func answer(t *testing.T, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}

func userSays(s string) agent.Message {
	return agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: s}}}
}

func TestCompleteMapsAPlainAnswer(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("42", nil, "stop")))

	got, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe", MaxTokens: 256},
		Messages: []agent.Message{userSays("what?")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.Message.Text() != "42" {
		t.Errorf("text = %q", got.Message.Text())
	}
	if got.Finish != agent.FinishStop {
		t.Errorf("Finish = %q", got.Finish)
	}
	want := agent.Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 80}
	if got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
	if sent()["model"] != "probe" {
		t.Errorf("model = %v", sent()["model"])
	}
	if sent()["max_completion_tokens"] != float64(256) {
		t.Errorf("max_completion_tokens = %v", sent()["max_completion_tokens"])
	}
}

// A temperature of 0 is a real choice, and a plain float64 would make it
// indistinguishable from unset. This is the test for that pointer.
func TestATemperatureOfZeroIsSent(t *testing.T) {
	t.Parallel()

	zero := 0.0
	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe", Temperature: &zero},
		Messages: []agent.Message{userSays("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, ok := sent()["temperature"]; !ok {
		t.Fatal("temperature 0 was dropped as if it had not been set")
	}
	if sent()["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", sent()["temperature"])
	}
}

func TestAnUnsetTemperatureIsNotSent(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := sent()["temperature"]; ok {
		t.Error("an unset temperature reached the wire")
	}
}

// The whole reason Content is a struct rather than a string. Without this,
// prompt caching silently does nothing and only the bill says so.
func TestACacheableBlockCarriesCacheControl(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		System: []agent.Content{
			{Type: agent.ContentText, Text: "you are a helpful agent", Cacheable: true},
		},
		Messages: []agent.Message{userSays("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	if !strings.Contains(string(raw), `"cache_control"`) {
		t.Fatalf("no cache_control reached the wire:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"ephemeral"`) {
		t.Errorf("cache_control carried no type:\n%s", raw)
	}
}

func TestAnImageTravelsAsADataURI(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("a cat", nil, "stop")))
	png := []byte{0x89, 0x50, 0x4e, 0x47}

	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png", Data: png}},
				{Type: agent.ContentText, Text: "what is this?"},
			},
		}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	if !strings.Contains(string(raw), "data:image/png;base64,iVBORw==") {
		t.Fatalf("the image did not travel as a data URI:\n%s", raw)
	}
}

func TestAFileTravelsWithItsName(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("read it", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{{
				Type: agent.ContentFile,
				Blob: &agent.Blob{MediaType: "application/pdf", Name: "q3.pdf", Data: []byte("%PDF")},
			}},
		}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	if !strings.Contains(string(raw), "q3.pdf") {
		t.Errorf("the filename did not reach the model, and it is context the model uses:\n%s", raw)
	}
}

// Arguments must survive byte for byte. Decoding to a map and re-encoding turns
// 1 into 1.0 and reorders keys, which reaches the tool as arguments it was never
// sent.
func TestToolCallArgumentsSurviveUntouched(t *testing.T) {
	t.Parallel()

	args := `{"repo":"openarity","count":1,"deep":true}`
	c, _ := serve(t, answer(t, completion("", []map[string]any{{
		"id": "call_1", "type": "function",
		"function": map[string]any{"name": "list_issues", "arguments": args},
	}}, "tool_calls")))

	got, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(got.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(got.Message.ToolCalls))
	}
	call := got.Message.ToolCalls[0]
	if string(call.Arguments) != args {
		t.Errorf("arguments = %s, want them byte for byte", call.Arguments)
	}
	if call.ID != "call_1" || call.Name != "list_issues" {
		t.Errorf("call = %+v", call)
	}
	if got.Finish != agent.FinishToolCalls {
		t.Errorf("Finish = %q, want tool_calls", got.Finish)
	}
}

func TestToolsReachTheWireWithTheirSchema(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("go")},
		Tools: []agent.Tool{{
			Name:        "list_issues",
			Description: "List issues in a repository",
			Schema:      json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`),
		}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	for _, want := range []string{"list_issues", "List issues in a repository", `"repo"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the tool definition is missing %q:\n%s", want, raw)
		}
	}
}

// A schema that is not an object would be rejected by the provider with an
// error naming neither the tool nor the reason.
func TestAnUnparsableSchemaNamesItsTool(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("go")},
		Tools:    []agent.Tool{{Name: "broken", Schema: json.RawMessage(`{"type":`)}},
	})
	if err == nil {
		t.Fatal("an unparsable schema was accepted")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("the error does not name the tool: %v", err)
	}
}

// A tool result whose call is missing from the history is rejected by the
// provider rather than ignored, so the assistant turn has to be replayed whole.
func TestAnAssistantTurnIsReplayedWithItsToolCalls(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("done", nil, "stop")))
	history := []agent.Message{
		userSays("look it up"),
		{
			Role:      agent.RoleAssistant,
			Content:   []agent.Content{{Type: agent.ContentText, Text: "checking"}},
			ToolCalls: []agent.ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"id":1}`)}},
		},
		{
			Role:       agent.RoleTool,
			ToolCallID: "call_1",
			Content:    []agent.Content{{Type: agent.ContentText, Text: "the answer"}},
		},
	}

	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: history,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	for _, want := range []string{`"tool_calls"`, "call_1", "lookup", `"role":"tool"`, "the answer"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the replayed history is missing %q:\n%s", want, raw)
		}
	}
}

func TestFinishReasonsAreMapped(t *testing.T) {
	t.Parallel()

	for wire, want := range map[string]agent.FinishReason{
		"stop":           agent.FinishStop,
		"tool_calls":     agent.FinishToolCalls,
		"function_call":  agent.FinishToolCalls,
		"length":         agent.FinishLength,
		"max_tokens":     agent.FinishLength,
		"content_filter": agent.FinishFilter,
		// Gateways invent reasons. Refusing a completed turn over a label we
		// have not seen throws away an answer the model produced.
		"something_new": agent.FinishStop,
	} {
		t.Run(wire, func(t *testing.T) {
			t.Parallel()

			c, _ := serve(t, answer(t, completion("x", nil, wire)))
			got, err := c.Complete(t.Context(), agent.Request{
				Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got.Finish != want {
				t.Errorf("%q mapped to %q, want %q", wire, got.Finish, want)
			}
		})
	}
}

func TestAResponseWithNoChoicesIsAnError(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, `{"id":"x","object":"chat.completion","choices":[]}`))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	}); err == nil {
		t.Fatal("a response with no choices was accepted")
	}
}

// A blob in the system prompt or a tool result cannot be sent, and dropping it
// silently would leave the model reasoning about something it never saw.
func TestABlobWhereOnlyTextIsAllowedIsRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		System: []agent.Content{
			{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png"}},
		},
		Messages: []agent.Message{userSays("go")},
	})
	if err == nil {
		t.Fatal("an image in the system prompt was accepted")
	}
}

func TestAnImageBlockWithNoBlobIsRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: agent.ContentImage}},
		}},
	})
	if err == nil {
		t.Fatal("an image block with no bytes was accepted")
	}
}

func TestAnUnknownRoleIsRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{Role: "narrator", Content: []agent.Content{{Type: agent.ContentText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("a message with an unknown role was accepted")
	}
	if !strings.Contains(err.Error(), "narrator") {
		t.Errorf("the error does not name the role: %v", err)
	}
}

func sse(t *testing.T, chunks ...string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("the test server cannot flush, so nothing would stream")
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

func chunk(delta map[string]any, finish string) string {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	out, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": "probe",
		"choices": []any{choice},
	})
	return string(out)
}

func drain(t *testing.T, s agent.Stream) (text string, final *agent.Response) {
	t.Helper()
	for s.Next() {
		ev := s.Event()
		text += ev.Text
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	return text, final
}

func TestStreamingYieldsDeltasThenAFinalResponse(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "content": "hello"}, ""),
		chunk(map[string]any{"content": " "}, ""),
		chunk(map[string]any{"content": "world"}, ""),
		chunk(map[string]any{}, "stop"),
	))

	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	text, final := drain(t, s)
	if text != "hello world" {
		t.Errorf("the deltas joined to %q", text)
	}
	if final == nil {
		t.Fatal("the stream ended without a final response")
	}
	if final.Message.Text() != "hello world" {
		t.Errorf("the final message says %q", final.Message.Text())
	}
	if final.Finish != agent.FinishStop {
		t.Errorf("Finish = %q", final.Finish)
	}
	if sent()["stream"] != true {
		t.Error("the request did not ask for a stream")
	}
}

// The reason this package exists rather than the pattern parsing SSE: a tool call
// arrives split across chunks, and dispatching before the last fragment lands
// runs something the model never finished asking for.
func TestAToolCallSplitAcrossChunksIsReassembled(t *testing.T) {
	t.Parallel()

	frag := func(s string) map[string]any {
		return map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"arguments": s},
		}}}
	}

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "list_issues", "arguments": ""},
		}}}, ""),
		chunk(frag(`{"re`), ""),
		chunk(frag(`po":"open`), ""),
		chunk(frag(`arity"}`), ""),
		chunk(map[string]any{}, "tool_calls"),
	))

	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	text, final := drain(t, s)
	if text != "" {
		t.Errorf("a tool-call-only turn emitted text %q", text)
	}
	if final == nil {
		t.Fatal("the stream ended without a final response")
	}
	if len(final.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(final.Message.ToolCalls), final.Message.ToolCalls)
	}

	call := final.Message.ToolCalls[0]
	if call.Name != "list_issues" || call.ID != "call_1" {
		t.Errorf("call = %+v", call)
	}
	if got := string(call.Arguments); got != `{"repo":"openarity"}` {
		t.Errorf("arguments reassembled to %s", got)
	}
	if !json.Valid(call.Arguments) {
		t.Error("the reassembled arguments are not valid JSON")
	}
	if final.Finish != agent.FinishToolCalls {
		t.Errorf("Finish = %q", final.Finish)
	}
}

// Without stream_options.include_usage the final chunk carries no usage and
// every run reports zero tokens, which reads as a free model.
func TestStreamingAsksForUsage(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, sse(t, chunk(map[string]any{"content": "hi"}, "stop")))
	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()
	drain(t, s)

	opts, ok := sent()["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("no stream_options were sent: %v", sent())
	}
	if opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage", opts)
	}
}

func TestAStreamThatFailsIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"gateway exploded"}}`)
	})

	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		return // refused at construction, which is also correct
	}
	defer s.Close()

	for s.Next() {
		if ev := s.Event(); ev.Final != nil {
			t.Fatal("a failed stream produced a final response")
		}
	}
	if s.Err() == nil {
		t.Fatal("a 500 ended the stream with no error")
	}
}

func TestCompleteReportsAFailedRequest(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	})

	_, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err == nil {
		t.Fatal("a 429 was reported as success")
	}
}

// One client serves every agent in the process. Under -race this catches a
// field that stops being read-only.
func TestTheClientIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("ok", nil, "stop")))

	done := make(chan error, 20)
	for i := range 20 {
		go func() {
			_, err := c.Complete(context.Background(), agent.Request{
				Model:    agent.ModelRef{Name: fmt.Sprintf("model-%d", i)},
				Messages: []agent.Message{userSays("go")},
			})
			done <- err
		}()
	}
	for range 20 {
		if err := <-done; err != nil {
			t.Errorf("a concurrent call failed: %v", err)
		}
	}
}

// The conversion runs before the request opens, so a bad tool is refused
// without a connection being made — same as the unary path.
func TestStreamRefusesABadRequestBeforeConnecting(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t, chunk(map[string]any{"content": "unreachable"}, "stop")))
	_, err := c.Stream(t.Context(), agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("go")},
		Tools:    []agent.Tool{{Name: "broken", Schema: json.RawMessage(`{"type":`)}},
	})
	if err == nil {
		t.Fatal("Stream accepted a tool with an unparsable schema")
	}
}

// A stream that carried no choices at all accumulates into nothing. Reporting
// it as a finished turn would hand the pattern an empty assistant message.
func TestAStreamWithNoChoicesIsReported(t *testing.T) {
	t.Parallel()

	empty, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": "probe", "choices": []any{},
	})
	c, _ := serve(t, sse(t, string(empty)))

	s, err := c.Stream(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{userSays("go")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	for s.Next() {
		if ev := s.Event(); ev.Final != nil {
			t.Fatal("a stream with no choices produced a final response")
		}
	}
	if s.Err() == nil {
		t.Fatal("a stream that accumulated nothing ended without an error")
	}
}

// A system message inside the history, rather than in Request.System. The brain
// may carry one either way and both have to reach the wire.
func TestASystemMessageInTheHistoryIsSent(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: []agent.Content{{Type: agent.ContentText, Text: "be terse"}}},
			userSays("go"),
		},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	if !strings.Contains(string(raw), "be terse") {
		t.Errorf("the system message did not reach the wire:\n%s", raw)
	}
}

// An unknown content type must be named, not dropped. Dropping it leaves the
// model answering about something it never received.
func TestAnUnknownContentTypeIsRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: "hologram", Text: "?"}},
		}},
	})
	if err == nil {
		t.Fatal("an unknown content type was accepted")
	}
	if !strings.Contains(err.Error(), "hologram") {
		t.Errorf("the error does not name the type: %v", err)
	}
}

// The remaining places a blob cannot go. Each would otherwise be dropped, and a
// dropped block is a model reasoning about something it never saw.
func TestABlobIsRefusedEverywhereOnlyTextIsAllowed(t *testing.T) {
	t.Parallel()

	blob := agent.Content{Type: agent.ContentImage, Blob: &agent.Blob{MediaType: "image/png"}}

	for _, tc := range []struct {
		name string
		msg  agent.Message
	}{
		{"in a system message", agent.Message{Role: agent.RoleSystem, Content: []agent.Content{blob}}},
		{"in a tool result", agent.Message{Role: agent.RoleTool, ToolCallID: "call_1", Content: []agent.Content{blob}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
			if _, err := c.Complete(t.Context(), agent.Request{
				Model: agent.ModelRef{Name: "probe"}, Messages: []agent.Message{tc.msg},
			}); err == nil {
				t.Fatal("a blob was accepted where only text can go")
			}
		})
	}
}

func TestAFileBlockWithNoBlobIsRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, answer(t, completion("unreachable", nil, "stop")))
	_, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role:    agent.RoleUser,
			Content: []agent.Content{{Type: agent.ContentFile}},
		}},
	})
	if err == nil {
		t.Fatal("a file block with no bytes was accepted")
	}
}

// A breakpoint can sit on a user turn as well as the system prompt — a long
// pasted document is exactly the case worth caching, and it arrives as a
// message rather than as identity.
func TestACacheableUserBlockCarriesCacheControl(t *testing.T) {
	t.Parallel()

	c, sent := serve(t, answer(t, completion("ok", nil, "stop")))
	if _, err := c.Complete(t.Context(), agent.Request{
		Model: agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{
				{Type: agent.ContentText, Text: "a very long document", Cacheable: true},
				{Type: agent.ContentText, Text: "summarise it"},
			},
		}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	raw, _ := json.Marshal(sent())
	if !strings.Contains(string(raw), `"cache_control"`) {
		t.Fatalf("a cacheable user block lost its breakpoint:\n%s", raw)
	}
}

// idChunk is chunk with the completion id spelled out, because the bugs below
// are about a gateway that changes it part-way through a response.
func idChunk(id string, delta map[string]any, finish string, usage map[string]any) string {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	body := map[string]any{
		"id": id, "object": "chat.completion.chunk", "model": "probe",
		"choices": []any{choice},
	}
	if usage != nil {
		body["usage"] = usage
	}
	out, _ := json.Marshal(body)
	return string(out)
}

// A gateway that renumbers between the content and the trailing usage makes
// openai-go's accumulator discard everything it held. Before this was tracked
// separately the run came back with an empty answer, no tokens, and nothing
// that looked like a failure — which is worse than an error, because a caller
// stores it as the agent's reply.
func TestAnAnswerSurvivesTheCompletionIdChangingMidStream(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		idChunk("a", map[string]any{"role": "assistant", "content": "the answer"}, "", nil),
		idChunk("a", map[string]any{}, "stop", nil),
		idChunk("z", map[string]any{}, "", map[string]any{
			"prompt_tokens": 90, "completion_tokens": 12,
		}),
	))

	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	text, final := drain(t, s)
	if text != "the answer" {
		t.Errorf("deltas = %q", text)
	}
	if final == nil {
		t.Fatal("no final response")
	}
	if got := final.Message.Text(); got != "the answer" {
		t.Errorf("final text = %q, want what the deltas carried", got)
	}
	if final.Usage.InputTokens != 90 || final.Usage.OutputTokens != 12 {
		t.Errorf("Usage = %+v, want the trailing chunk's", final.Usage)
	}
}

// Half a reply is worse than none: nothing about it looks wrong, and it is
// stored as what the agent said. So what the chunks carried is preferred over
// what the accumulator ended up holding, rather than used only as a fallback.
func TestTheWholeAnswerSurvivesWhenTheIdChangesPartWayThrough(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		idChunk("a", map[string]any{"role": "assistant", "content": "first half"}, "", nil),
		idChunk("b", map[string]any{"content": " and second"}, "", nil),
		idChunk("b", map[string]any{}, "stop", map[string]any{
			"prompt_tokens": 90, "completion_tokens": 12,
		}),
	))

	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	_, final := drain(t, s)
	if final == nil {
		t.Fatal("no final response")
	}
	if got := final.Message.Text(); got != "first half and second" {
		t.Errorf("final text = %q, want the whole answer", got)
	}
}

// A lost finish reason converts to FinishStop, and a turn truncated at
// MaxTokens then looks like a turn that finished — which is exactly the guard
// the loop uses to refuse dispatching a half-written tool call.
func TestATruncatedTurnStaysTruncatedWhenTheAccumulatorLosesIt(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		idChunk("a", map[string]any{"role": "assistant", "content": "half a sen"}, "", nil),
		idChunk("a", map[string]any{}, "length", nil),
		idChunk("z", map[string]any{}, "", map[string]any{
			"prompt_tokens": 90, "completion_tokens": 1024,
		}),
	))

	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	_, final := drain(t, s)
	if final == nil {
		t.Fatal("no final response")
	}
	if final.Finish != agent.FinishLength {
		t.Errorf("Finish = %q, want length — a truncated turn that reads as finished is dispatched", final.Finish)
	}
}

// Tool call arguments arrive as fragments that mean nothing until they are
// whole, and reassembling them is the one job the accumulator keeps.
func TestToolCallsAreStillAssembledByTheAccumulator(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "count", "arguments": ""},
		}}}, ""),
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"arguments": `{"re`},
		}}}, ""),
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"arguments": `po":"x"}`},
		}}}, ""),
		chunk(map[string]any{}, "tool_calls"),
	))

	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	_, final := drain(t, s)
	if final == nil {
		t.Fatal("no final response")
	}
	if len(final.Message.ToolCalls) != 1 {
		t.Fatalf("%d tool calls, want 1", len(final.Message.ToolCalls))
	}
	call := final.Message.ToolCalls[0]
	if call.Name != "count" || string(call.Arguments) != `{"repo":"x"}` {
		t.Errorf("call = %s(%s), want the fragments joined", call.Name, call.Arguments)
	}
	if final.Finish != agent.FinishToolCalls {
		t.Errorf("Finish = %q, want tool_calls", final.Finish)
	}
}

// A stream that carried nothing at all is still a failure. Salvaging what the
// chunks held must not turn an empty response into an empty answer that looks
// deliberate.
func TestAStreamThatCarriedNothingIsStillRefused(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, sse(t,
		`{"id":"a","object":"chat.completion.chunk","model":"probe","choices":[]}`,
	))

	s, err := c.Stream(t.Context(), probeRequest())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close()

	for s.Next() {
		if ev := s.Event(); ev.Final != nil {
			t.Fatal("a stream that carried nothing produced a final response")
		}
	}
	if s.Err() == nil {
		t.Error("a stream that carried nothing was accepted")
	}
}

// probeRequest is the smallest request the streaming tests need: what they are
// about is what comes back, not what went out.
func probeRequest() agent.Request {
	return agent.Request{
		Model:    agent.ModelRef{Name: "probe"},
		Messages: []agent.Message{userSays("go")},
	}
}
