// Command steering-typed lets you type at an agent while it is working.
//
// This is the shape most programs steering an agent actually want: a person
// watching a run, seeing it head the wrong way, and saying so — without
// stopping it and without starting again.
//
// Nothing is interrupted. What you type waits, and rides the next request out.
// The tool in flight finishes first, which is why the prompt appears while the
// search is still running and your words still arrive in time.
//
//	go run ./examples/steering-typed
//
// Reading your keystrokes is opt-in:
//
//	STEER_FROM_STDIN=1 go run ./examples/steering-typed
//
// Without it the example steers with a scripted line, through exactly the same
// call. That is not a convenience — `make example` inherits the terminal it
// was started from, so an example that always read stdin would hang the whole
// suite waiting for input nobody knew to give it.
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

func main() {
	// os.Exit skips deferred calls, so the signal handler is released in
	// attempt rather than in a defer here that would never run.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	// Ctrl-C matters here because a streaming run holds an open connection.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("search", `{"query":"failing test"}`),
		gateway.Answer("The failure is in vault.go, not in the tests."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	// The tool waits for this, so there is always a moment to type into. A
	// real tool is slow on its own; this one has to be told to be.
	typed := make(chan struct{})

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{search(typed)},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	reported := make(chan struct{})

	// Closed when the model first reaches for a tool — the moment a person
	// watching would see it heading the wrong way. The scripted line waits for
	// it so the example shows what it claims to: a steer arriving *during* the
	// work, landing on the request after it rather than the one before.
	sawTool := make(chan struct{})

	run := runner.Start(ctx, spec, msgs, endpoint, events)

	go func() {
		defer close(reported)
		for ev := range events {
			switch e := ev.(type) {
			case agent.ToolCallEvent:
				fmt.Printf("tool     %s %s\n", e.Name, e.Arguments)
				close(sawTool)
			case agent.SteerEvent:
				fmt.Printf("steer    reached a request: %q\n", e.Text)
			}
		}
	}()

	// One goroutine, one source of text, one call to Steer. Whether the line
	// came from a person or from the fallback makes no difference past here.
	go func() {
		defer close(typed)
		for _, line := range lines(ctx, sawTool) {
			if err := run.Steer(line); err != nil {
				// ErrRunFinished is the ordinary ending, not a failure: the
				// run finished while somebody was still typing.
				fmt.Printf("steer    not applied — %v\n", err)
				return
			}
			fmt.Printf("steer    queued\n")
		}
	}()

	result, err := run.Wait()
	close(events)
	<-reported
	if err != nil {
		return err
	}

	if len(result.UnappliedSteers) > 0 {
		fmt.Printf("\nunapplied  %d — the run ended before a request could carry them\n",
			len(result.UnappliedSteers))
	}
	gateway.Summary(result)
	return nil
}

// lines is what to steer with: whatever is typed, or one scripted line.
//
// Returning a slice rather than a channel keeps the caller identical in both
// cases. Neither source can produce more than a handful of lines before the
// run is over, so nothing is gained by streaming them.
func lines(ctx context.Context, sawTool <-chan struct{}) []string {
	if os.Getenv("STEER_FROM_STDIN") == "" {
		fmt.Println("steering with a scripted line — set STEER_FROM_STDIN=1 to type your own")
		fmt.Println()

		// Where a person's hesitation would go.
		select {
		case <-sawTool:
		case <-ctx.Done():
			return nil
		}
		return []string{"the failure is in vault.go, stop reading tests"}
	}

	fmt.Println("Type to steer it, Enter to send. Ctrl-D when you are done.")
	fmt.Print("> ")

	var out []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return out
		}
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
		fmt.Print("> ")
	}
	return out
}

func search(typed <-chan struct{}) agent.Tool {
	return agent.Tool{
		Name:        "search",
		Description: "Search the codebase.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string"}},
			"required": ["query"]
		}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			// A real tool would be doing the work here — and that is the
			// window a person types into.
			select {
			case <-typed:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "12 files match", nil
		},
	}
}
