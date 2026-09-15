// Command steering says something to a run that has already started.
//
// The model is not listening while a pattern works: it is called, it answers,
// and between those two moments it has no ears. So a steer is not delivered —
// it waits, and rides the next request out.
//
// This prints the message that carried it, which is the part a reader would
// otherwise have to take on trust: the steer is not a message of its own. It
// is added to the end of the tool result, because a provider requires the
// message after a tool call to be that call's result and nothing may be
// wedged between them.
//
//	go run ./examples/steering
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
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

	// The factory is the caller's, so wrapping it is the caller's to do. This
	// records what each request actually carried — the only way an example can
	// show where a steer landed without reaching inside the library.
	seen := &recorder{}
	runner, err := agent.New(seen.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	// Closed once the steer has been sent. The tool waits for it so the
	// example is the same every run — standing in for the real case, which is
	// a person typing while a slow tool works.
	steered := make(chan struct{})

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{search(steered)},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})

	// Start rather than Run: it hands back a handle while the run works.
	run := runner.Start(ctx, spec, msgs, endpoint, events)

	go func() {
		defer close(done)
		for ev := range events {
			switch e := ev.(type) {
			case agent.ToolCallEvent:
				// The moment a person watching would realise it is looking in
				// the wrong place.
				fmt.Printf("tool     %s — steering it away\n", e.Name)
				if err := run.Steer("the failure is in vault.go, stop reading tests"); err != nil {
					fmt.Fprintln(os.Stderr, "steer:", err)
				}
				close(steered)
			case agent.SteerEvent:
				fmt.Printf("steer    reached a request: %q\n", e.Text)
			}
		}
	}()

	result, err := run.Wait()
	close(events)
	<-done
	if err != nil {
		return err
	}

	report(seen, result)
	return nil
}

// report prints the two facts the example exists for.
func report(seen *recorder, result agent.Result) {
	fmt.Println()

	// One: the steer is not a message. It is on the end of the tool result,
	// where a provider will accept it.
	if req := seen.last(); len(req) > 0 {
		carrier := req[len(req)-1]
		fmt.Printf("carried by  a %s message, not one of its own:\n", carrier.Role)
		for _, line := range strings.Split(strings.TrimSpace(carrier.Text()), "\n") {
			fmt.Printf("    │ %s\n", line)
		}
	}

	// Two: it is not in the transcript. The pattern's own slice was never
	// touched, so handing Result.Messages back on the next turn does not
	// replay the steer as something the user said again.
	fmt.Printf("\ntranscript  %d messages, and the steer is in %d of them\n",
		len(result.Messages), countSteers(result.Messages))

	if len(result.UnappliedSteers) > 0 {
		fmt.Printf("unapplied   %d — the run ended before a request could carry them\n",
			len(result.UnappliedSteers))
	}

	gateway.Summary(result)
}

func countSteers(msgs []agent.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Text(), "stop reading tests") {
			n++
		}
	}
	return n
}

func search(steered <-chan struct{}) agent.Tool {
	return agent.Tool{
		Name:        "search",
		Description: "Search the codebase.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string"}},
			"required": ["query"]
		}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			// A real tool would do the work here. This one waits for the
			// steer, so the run is identical every time.
			select {
			case <-steered:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "12 files match", nil
		},
	}
}

// recorder keeps the messages of every request, so the example can show what
// the model was sent rather than assert it.
type recorder struct {
	mu   sync.Mutex
	sent [][]agent.Message
}

func (r *recorder) wrap(inner agent.ClientFactory) agent.ClientFactory {
	return func(e agent.Endpoint) (agent.ModelClient, error) {
		client, err := inner(e)
		if err != nil {
			return nil, err
		}
		return &recordingClient{inner: client, to: r}, nil
	}
}

func (r *recorder) add(msgs []agent.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, msgs)
}

func (r *recorder) last() []agent.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sent) == 0 {
		return nil
	}
	return r.sent[len(r.sent)-1]
}

type recordingClient struct {
	inner agent.ModelClient
	to    *recorder
}

func (c *recordingClient) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	c.to.add(req.Messages)
	return c.inner.Complete(ctx, req)
}

func (c *recordingClient) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	c.to.add(req.Messages)
	return c.inner.Stream(ctx, req)
}
