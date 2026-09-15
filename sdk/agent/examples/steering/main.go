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
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("search", `{"query":"failing test"}`),
		gateway.Answer("The failure is in vault.go, not in the tests."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	seen := &recorder{}
	runner, err := agent.New(seen.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	steered := make(chan struct{})
	var once sync.Once

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant. Always call the search tool before answering — never answer from memory."),
		Tools:    []agent.Tool{search(steered)},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})

	run := runner.Start(ctx, spec, msgs, endpoint, events)

	go func() {
		defer close(done)
		for ev := range events {
			switch e := ev.(type) {
			case agent.ToolCallEvent:
				fmt.Printf("tool     %s\n", e.Name)
				once.Do(func() {
					fmt.Println("steer    sending it away from the tests")
					if err := run.Steer("the failure is in vault.go, stop reading tests"); err != nil {
						fmt.Fprintln(os.Stderr, "steer:", err)
					}
					close(steered)
				})
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

func report(seen *recorder, result agent.Result) {
	fmt.Println()

	req := seen.last()
	for i, m := range req {
		if !strings.Contains(m.Text(), "stop reading tests") {
			continue
		}
		fmt.Printf("carried by  a %s message of its own, at index %d of %d:\n",
			m.Role, i, len(req))
		for _, line := range strings.Split(strings.TrimSpace(m.Text()), "\n") {
			fmt.Printf("    │ %s\n", line)
		}
		if i+1 < len(req) {
			fmt.Printf("\nstill last  a %s message — the steer did not follow the end of\n"+
				"            the conversation, so the model could stop and answer\n",
				req[len(req)-1].Role)
		}
		break
	}

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
		Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
			select {
			case <-steered:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			var in struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			if strings.Contains(in.Query, "vault") {
				return "vault.go:88  renewLease returns a nil lease when the token is already expired", nil
			}
			return "12 files match", nil
		},
	}
}

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
