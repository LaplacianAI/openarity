// Command examples runs one agent turn end to end.
//
// With no configuration it starts a stub gateway in-process, so it works
// offline and needs nothing installed:
//
//	go run ./examples
//
// Point it at a real gateway — LiteLLM, OmniRoute, or a provider — with two
// environment variables:
//
//	OPENARITY_MODELS_BASE_URL=http://127.0.0.1:4000/v1 \
//	OPENARITY_MODELS_API_KEY=sk-… \
//	OPENARITY_MODEL=anthropic/claude-opus-5 \
//	go run ./examples
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/loops"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

func main() {
	// os.Exit skips deferred calls, so the signal handler is released here
	// rather than in a defer that would never run on the failing path.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// attempt owns the signal handler so main can exit non-zero without leaking it.
// Ctrl-C matters here because a streaming run holds an open connection.
func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run(ctx)
}

func run(ctx context.Context) error {
	endpoint, shutdown := endpoint()
	defer shutdown()

	model := os.Getenv("OPENARITY_MODEL")
	if model == "" {
		model = "anthropic/claude-opus-5"
	}

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, model)

	// Wired once. A real deployment does this at startup and keeps the Runner
	// for the life of the process.
	runner, err := agent.New(openaicompat.Factory(), loops.ReActStreaming())
	if err != nil {
		return err
	}

	// Everything here is what the brain resolves before calling: which model,
	// which loop, which tools, and a ceiling. The tool's closure would reach an
	// MCP server under a team's credential; here it counts to three.
	spec := agent.Spec{
		Model:    agent.ModelRef{Name: model, MaxTokens: 1024},
		Loop:     agent.LoopReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{countIssues()},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "How many open issues does openarity have?"}},
	}}

	// Events are consumed on their own goroutine. The loop never blocks on a
	// slow reader — Emit gives up when the context is done — but a reader that
	// never starts would still see nothing.
	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	if err != nil {
		return err
	}

	fmt.Printf("\n\nanswer   %s\nsteps    %d\ntokens   %d in (%d cached), %d out\n",
		result.Output, result.Steps,
		result.Usage.InputTokens, result.Usage.CachedInputTokens, result.Usage.OutputTokens)
	return nil
}

// report prints the run as it happens. A dashboard or a channel adapter reads
// the same channel; this is the smallest consumer that shows every kind.
func report(events <-chan agent.Event) {
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
			fmt.Printf("\n  ← %s: %s (%s)", ev.Name, ev.Output, ev.Duration.Round(time.Microsecond))
		case agent.UsageEvent:
			fmt.Printf("\n  [%d in, %d out]", ev.Usage.InputTokens, ev.Usage.OutputTokens)
		}
	}
}

func countIssues() agent.Tool {
	return agent.Tool{
		Name:        "count_issues",
		Description: "Count the open issues in a repository. Use when asked how many issues exist.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"repo": {"type": "string", "description": "the repository name"}},
			"required": ["repo"]
		}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Repo string `json:"repo"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				// Returned, not failed: the model reads this and can correct
				// itself. Only a cancelled context ends a run.
				return "", fmt.Errorf("could not read the arguments: %w", err)
			}
			return fmt.Sprintf("%s has 3 open issues", in.Repo), nil
		},
	}
}

// endpoint reads the environment, falling back to a stub gateway so the example
// runs with nothing installed. The shutdown function is a no-op for a real one.
func endpoint() (agent.Endpoint, func()) {
	if url := os.Getenv("OPENARITY_MODELS_BASE_URL"); url != "" {
		return agent.Endpoint{BaseURL: url, APIKey: os.Getenv("OPENARITY_MODELS_API_KEY")}, func() {}
	}

	srv := httptest.NewServer(stubGateway())
	fmt.Println("no OPENARITY_MODELS_BASE_URL set — using an in-process stub gateway")
	return agent.Endpoint{BaseURL: srv.URL, APIKey: "sk-stub"}, srv.Close
}

// stubGateway speaks enough of the OpenAI streaming protocol to drive a real
// two-step run: first a tool call split across chunks, then an answer.
func stubGateway() http.Handler {
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

		if calls.Add(1) == 1 {
			// Arguments arrive in fragments, which is the case the accumulator
			// exists for: nothing is dispatchable until the last one lands.
			send(
				chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"count_issues","arguments":""}}]}`, ""),
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"re"}}]}`, ""),
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"po\":\"open"}}]}`, ""),
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"arity\"}"}}]}`, ""),
				chunk(`{}`, "tool_calls"),
			)
			return
		}

		send(
			chunk(`{"role":"assistant","content":"openarity has "}`, ""),
			chunk(`{"content":"3 open "}`, ""),
			chunk(`{"content":"issues."}`, ""),
			chunk(`{}`, "stop"),
		)
	})
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
