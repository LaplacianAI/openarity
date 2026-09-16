package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
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

	if err := plannedUpFront(ctx); err != nil {
		return err
	}
	return tooLate(ctx)
}

func plannedUpFront(ctx context.Context) error {
	fmt.Println("── ReWOO: the plan is already fixed ──────────────────────")

	endpoint, shutdown := gateway.Resolve(

		gateway.ToolCall(patterns.PlanToolName,
			`{"steps":[{"tool":"search","args":{"query":"failing test"},`+
				`"why":"find where the failure is"}]}`),
		gateway.Answer("The failure is in vault.go, not in the tests."),
	)
	defer shutdown()

	runner, err := agent.New(openaicompat.Factory(), patterns.ReWOOStreaming())
	if err != nil {
		return err
	}

	handle := make(chan *agent.Run, 1)

	var tools atomic.Int32

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReWOO,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{steeringTool(handle, &tools)},
		MaxSteps: 8,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if e, ok := ev.(agent.SteerEvent); ok {
				fmt.Printf("  steer reached a request: %q\n", e.Text)
			}
		}
	}()

	run := runner.Start(ctx, spec, msgs, endpoint, events)
	handle <- run

	result, err := run.Wait()
	close(events)
	<-done
	if err != nil {
		return err
	}

	fmt.Printf("  tools run  %d — the plan named one step, and a steer cannot\n", tools.Load())
	fmt.Printf("             add or remove one: it was decided a model call\n")
	fmt.Printf("             before the steer existed.\n")
	fmt.Printf("  answer     %s\n\n", summarise(result.Output))
	return nil
}

func tooLate(ctx context.Context) error {
	fmt.Println("── A steer with no request left to carry it ──────────────")

	endpoint, shutdown := gateway.Resolve(
		gateway.Answer("Nothing needed looking up."),
	)
	defer shutdown()

	handle := make(chan *agent.Run, 1)
	late := &lateSteerer{handle: handle}

	runner, err := agent.New(late.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant."),
		MaxSteps: 5,
	}
	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Say something."}},
	}}

	run := runner.Start(ctx, spec, msgs, endpoint, nil)
	handle <- run

	result, err := run.Wait()
	if err != nil {
		return err
	}

	fmt.Printf("  sent during the last call   %d unapplied: %q\n",
		len(result.UnappliedSteers), strings.Join(result.UnappliedSteers, ", "))

	err = run.Steer("later still")
	fmt.Printf("  sent after the run          %v\n", err)
	if !errors.Is(err, agent.ErrRunFinished) {
		return fmt.Errorf("expected ErrRunFinished, got: %w", err)
	}

	fmt.Println()
	gateway.Summary(result)
	return nil
}

func steeringTool(handle <-chan *agent.Run, count *atomic.Int32) agent.Tool {
	return agent.Tool{
		Name:        "search",
		Description: "Search the codebase.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string"}},
			"required": ["query"]
		}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			count.Add(1)
			select {
			case run := <-handle:
				if err := run.Steer("the failure is in vault.go, stop reading tests"); err != nil {
					return "", err
				}
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "12 files match", nil
		},
	}
}

type lateSteerer struct {
	handle <-chan *agent.Run

	mu   sync.Mutex
	sent bool
}

func (l *lateSteerer) wrap(inner agent.ClientFactory) agent.ClientFactory {
	return func(e agent.Endpoint) (agent.ModelClient, error) {
		client, err := inner(e)
		if err != nil {
			return nil, err
		}
		return &lateClient{inner: client, steerer: l}, nil
	}
}

func (l *lateSteerer) steer(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent {
		return
	}
	l.sent = true

	select {
	case run := <-l.handle:
		_ = run.Steer("too late for this request")
	case <-ctx.Done():
	}
}

type lateClient struct {
	inner   agent.ModelClient
	steerer *lateSteerer
}

func (c *lateClient) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	c.steerer.steer(ctx)
	return c.inner.Complete(ctx, req)
}

func (c *lateClient) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	c.steerer.steer(ctx)
	return c.inner.Stream(ctx, req)
}

func summarise(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:57] + "…"
}
