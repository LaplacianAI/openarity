// Package gateway is the plumbing the examples share: where to send requests,
// and what to print when the answers come back.
//
// It exists so each example is the thing it demonstrates and nothing else. A
// reader comparing two examples should see one difference between them.
package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// A Turn is one scripted model response for the stub gateway. Either it asks
// for tools or it answers; a real turn can do both, but no example needs that.
type Turn struct {
	Text  string
	Calls []Call
}

// A Call is one tool the scripted turn asks for.
type Call struct {
	Name string
	Args string
}

// Answer scripts a turn that ends the run.
func Answer(text string) Turn { return Turn{Text: text} }

// ToolCall scripts a turn asking for one tool. Skills arrive this way too:
// ToolCall(agent.SkillToolName, `{"name":"commit-style"}`).
func ToolCall(name, args string) Turn { return Turn{Calls: []Call{{Name: name, Args: args}}} }

// ToolCalls scripts a turn asking for several tools at once, which is what a
// current model does with calls that do not depend on each other. Scripting
// them one per turn measures a model generation that has passed.
func ToolCalls(calls ...Call) Turn { return Turn{Calls: calls} }

// Model is the model to ask for, from the environment or a default that suits
// whichever gateway is in use.
func Model() string {
	if m := os.Getenv("OPENARITY_MODEL"); m != "" {
		return m
	}
	return "anthropic/claude-opus-5"
}

// Resolve points the example at a real gateway if the environment names one,
// and otherwise at a stub running in this process, so an example works with
// nothing installed. The returned function shuts the stub down; for a real
// gateway it does nothing.
//
//	OPENARITY_MODELS_BASE_URL=http://127.0.0.1:20128/v1 \
//	OPENARITY_MODELS_API_KEY=sk-… \
//	OPENARITY_MODEL=cc/claude-opus-4-8 \
//	go run ./examples/skills
func Resolve(script ...Turn) (agent.Endpoint, func()) {
	if url := os.Getenv("OPENARITY_MODELS_BASE_URL"); url != "" {
		return agent.Endpoint{BaseURL: url, APIKey: os.Getenv("OPENARITY_MODELS_API_KEY")}, func() {}
	}

	srv := httptest.NewServer(stub(script))
	fmt.Println("no OPENARITY_MODELS_BASE_URL set — using an in-process stub gateway")
	return agent.Endpoint{BaseURL: srv.URL, APIKey: "sk-stub"}, srv.Close
}

// Report prints a run as it happens and returns how many times the model was
// asked and what the last of those calls carried in. A dashboard or a channel adapter reads the same channel; this is the
// smallest consumer that shows every kind of event.
//
// Model calls are counted here rather than at the stub, because a real gateway
// never reaches the stub — and rather than from Result.Steps, because a step
// means something different in each pattern. Every pattern emits exactly one
// UsageEvent per model call.
func Report(events <-chan agent.Event) (modelCalls, lastInput int) {
	for e := range events {
		switch ev := e.(type) {
		case agent.StepEvent:
			fmt.Printf("\n[step %d] ", ev.Step)
		case agent.TextEvent:
			fmt.Print(ev.Delta)
		case agent.ToolCallEvent:
			fmt.Printf("\n  → %s(%s)", ev.Name, ev.Arguments)
		case agent.ToolResultEvent:
			if ev.Err != "" {
				fmt.Printf("\n  ← %s failed: %s", ev.Name, ev.Err)
				continue
			}
			fmt.Printf("\n  ← %s: %s (%s)", ev.Name, summarise(ev.Output), ev.Duration.Round(time.Microsecond))
		case agent.UsageEvent:
			modelCalls++
			lastInput = ev.Usage.InputTokens
			fmt.Printf("\n  [%d in, %d out]", ev.Usage.InputTokens, ev.Usage.OutputTokens)
		}
	}
	return modelCalls, lastInput
}

// Summary prints what a run cost. Cached input is here because it is the number
// the whole skill design is arranged around, and it is invisible otherwise.
func Summary(result agent.Result) {
	fmt.Printf("\n\nanswer   %s\nsteps    %d\ntokens   %d in (%d cached), %d out\n",
		result.Output, result.Steps,
		result.Usage.InputTokens, result.Usage.CachedInputTokens, result.Usage.OutputTokens)
}

// summarise keeps a loaded skill body from burying the run it appears in. A
// skill body is meant to be long; the point of printing it is that it arrived.
func summarise(s string) string {
	const limit = 60
	if len(s) <= limit {
		return s
	}
	return fmt.Sprintf("%.*s… (%d bytes)", limit, s, len(s))
}

// stub speaks enough of the OpenAI streaming protocol to drive a real run.
func stub(script []Turn) http.Handler {
	var calls atomic.Int32

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real gateway answers both shapes on one path, keyed on the request.
		// A stub that only streamed would make a non-streaming pattern look broken
		// with an error about content types rather than about the pattern.
		var req struct {
			Stream bool `json:"stream"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		// Past the script the stub stops rather than repeating its last turn,
		// which would loop until MaxSteps and read as a bug in the pattern rather
		// than a script that ran out.
		turn := Turn{Text: "the script ended"}
		if n := int(calls.Add(1)) - 1; n < len(script) {
			turn = script[n]
		}

		// Priced off the request rather than a flat rate, so a comparison
		// between two patterns measures the context each one carries instead
		// of only how many turns it took. Four bytes to a token is close
		// enough for English prose and JSON.
		spent := usage{
			PromptTokens:     len(body) / 4,
			CompletionTokens: 12,
		}
		spent.TotalTokens = spent.PromptTokens + spent.CompletionTokens

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, completion(turn, spent))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "cannot stream", http.StatusInternalServerError)
			return
		}

		send := func(chunks ...string) {
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
				flusher.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}

		send(turnChunks(turn, spent)...)
	})
}

func turnChunks(t Turn, spent usage) []string {
	if len(t.Calls) == 0 {
		return []string{
			chunk(delta{Role: "assistant", Content: t.Text}, "", nil),
			chunk(delta{}, "stop", &spent),
		}
	}

	opening := delta{Role: "assistant"}
	for i, c := range t.Calls {
		opening.ToolCalls = append(opening.ToolCalls, toolCall{
			Index: i, ID: fmt.Sprintf("call_%d", i+1), Type: "function",
			Function: function{Name: c.Name},
		})
	}
	out := []string{chunk(opening, "", nil)}

	// Arguments arrive in fragments, which is the case the accumulator exists
	// for: nothing is dispatchable until the last one lands. Splitting them
	// here is what makes the example exercise that rather than assert it.
	for i, c := range t.Calls {
		for _, part := range split(c.Args, 3) {
			out = append(out, chunk(delta{
				ToolCalls: []toolCall{{Index: i, Function: function{Arguments: part}}},
			}, "", nil))
		}
	}
	return append(out, chunk(delta{}, "tool_calls", &spent))
}

// split cuts a string into at most n pieces, on bytes, because that is what a
// gateway does: SSE chunk boundaries have nothing to do with JSON structure.
func split(s string, n int) []string {
	if s == "" || n < 2 {
		return []string{s}
	}
	size := (len(s) + n - 1) / n
	var out []string
	for i := 0; i < len(s); i += size {
		out = append(out, s[i:min(i+size, len(s))])
	}
	return out
}

// The wire types are declared rather than assembled from string fragments.
// Scripted text carries quotes and braces — a skill's arguments are themselves
// JSON — and a stub that concatenates its way to a chunk emits one the client
// cannot parse the first time somebody scripts an apostrophe.
type (
	completionChunk struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Model   string   `json:"model"`
		Choices []choice `json:"choices"`
		Usage   *usage   `json:"usage,omitempty"`
	}

	choice struct {
		Index        int    `json:"index"`
		Delta        delta  `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	}

	delta struct {
		Role      string     `json:"role,omitempty"`
		Content   string     `json:"content,omitempty"`
		ToolCalls []toolCall `json:"tool_calls,omitempty"`
	}

	toolCall struct {
		Index    int      `json:"index"`
		ID       string   `json:"id,omitempty"`
		Type     string   `json:"type,omitempty"`
		Function function `json:"function"`
	}

	// Arguments has no omitempty: the first fragment of a tool call carries an
	// empty string, and a client reading the field's absence as "no arguments"
	// would dispatch before any of them arrived.
	function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	}

	usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}
)

// chunk renders one SSE chunk. Usage rides the final one only: a gateway
// repeating it on every chunk would have the accumulator sum them, and the run
// would report five times the tokens it actually spent — worth getting right
// here, because an example is where somebody learns what to expect.
func chunk(d delta, finish string, spent *usage) string {
	c := completionChunk{
		ID:      "chatcmpl-stub",
		Object:  "chat.completion.chunk",
		Model:   "stub",
		Choices: []choice{{Delta: d, FinishReason: finish}},
		Usage:   spent,
	}

	b, err := json.Marshal(c)
	if err != nil {
		// Unreachable: every field is a string, an int or a slice of them.
		panic(err)
	}
	return string(b)
}

// completion renders one turn as a non-streaming response. The wire shape
// differs from a chunk in exactly one place — "message" rather than "delta" —
// which is why it reuses the same types.
func completion(t Turn, spent usage) string {
	m := delta{Role: "assistant", Content: t.Text}
	finish := "stop"

	if len(t.Calls) > 0 {
		// Whole, not fragmented: a non-streaming response has no chunks to
		// split across, and arriving in one piece is the difference the
		// streaming path exists to handle.
		m = delta{Role: "assistant"}
		for i, c := range t.Calls {
			m.ToolCalls = append(m.ToolCalls, toolCall{
				Index: i, ID: fmt.Sprintf("call_%d", i+1), Type: "function",
				Function: function{Name: c.Name, Arguments: c.Args},
			})
		}
		finish = "tool_calls"
	}

	type completionChoice struct {
		Index        int    `json:"index"`
		Message      delta  `json:"message"`
		FinishReason string `json:"finish_reason"`
	}

	b, err := json.Marshal(struct {
		ID      string             `json:"id"`
		Object  string             `json:"object"`
		Model   string             `json:"model"`
		Choices []completionChoice `json:"choices"`
		Usage   usage              `json:"usage"`
	}{
		ID: "chatcmpl-stub", Object: "chat.completion", Model: "stub",
		Choices: []completionChoice{{Message: m, FinishReason: finish}},
		Usage:   spent,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}
