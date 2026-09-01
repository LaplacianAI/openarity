// Package gateway is the plumbing the examples share: where to send requests,
// and what to print when the answers come back.
//
// It exists so each example is the thing it demonstrates and nothing else. A
// reader comparing two examples should see one difference between them.
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// A Turn is one scripted model response for the stub gateway. Either it calls a
// tool or it answers; a real turn can do both, but no example needs that yet.
type Turn struct {
	Text     string
	ToolName string
	ToolArgs string
}

// Answer scripts a turn that ends the run.
func Answer(text string) Turn { return Turn{Text: text} }

// ToolCall scripts a turn that asks for a tool. Skills arrive this way too:
// ToolCall(agent.SkillToolName, `{"name":"commit-style"}`).
func ToolCall(name, args string) Turn { return Turn{ToolName: name, ToolArgs: args} }

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

// Report prints a run as it happens. A dashboard or a channel adapter reads the
// same channel; this is the smallest consumer that shows every kind of event.
func Report(events <-chan agent.Event) {
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
			fmt.Printf("\n  [%d in, %d out]", ev.Usage.InputTokens, ev.Usage.OutputTokens)
		}
	}
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

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

		n := int(calls.Add(1)) - 1
		if n >= len(script) {
			// Past the script the stub stops rather than repeating its last
			// turn, which would loop until MaxSteps and look like a bug in the
			// loop rather than a script that ran out.
			send(chunk(`{"role":"assistant","content":"the script ended"}`, "stop"))
			return
		}
		send(turnChunks(script[n])...)
	})
}

func turnChunks(t Turn) []string {
	if t.ToolName == "" {
		return []string{
			chunk(`{"role":"assistant","content":`+quote(t.Text)+`}`, ""),
			chunk(`{}`, "stop"),
		}
	}

	// Arguments arrive in fragments, which is the case the accumulator exists
	// for: nothing is dispatchable until the last one lands. Splitting them
	// here is what makes the example exercise that rather than assert it.
	out := []string{
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":`+
			quote(t.ToolName)+`,"arguments":""}}]}`, ""),
	}
	for _, part := range split(t.ToolArgs, 3) {
		out = append(out, chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+quote(part)+`}}]}`, ""))
	}
	return append(out, chunk(`{}`, "tool_calls"))
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

func chunk(delta, finish string) string {
	choice := `{"index":0,"delta":` + delta
	if finish != "" {
		choice += `,"finish_reason":"` + finish + `"`
	}
	choice += "}"

	body := `{"id":"chatcmpl-stub","object":"chat.completion.chunk","model":"stub","choices":[` + choice + `]`

	// Usage rides the final chunk only. A gateway repeating it on every chunk
	// would have the accumulator sum them, and the run would report five times
	// the tokens it actually spent — worth getting right here, because an
	// example is where somebody learns what to expect.
	if finish != "" {
		body += `,"usage":{"prompt_tokens":90,"completion_tokens":12,"total_tokens":102}`
	}
	return body + "}"
}

// quote produces a JSON string. The scripted text contains braces and quotes —
// a skill's arguments are themselves JSON — so building it by hand is how the
// stub ends up emitting a chunk the client cannot parse.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
