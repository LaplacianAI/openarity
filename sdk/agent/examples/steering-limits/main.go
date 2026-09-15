// Command steering-limits shows the two places steering stops doing what you
// might hope, both of which the SDK makes visible rather than silent.
//
// One — a pattern that plans up front is steerable, but not in the way a
// reader expects. ReWOO decides every tool call in one model call before any
// of them runs, so a steer arriving during execution cannot change the plan.
// It reaches the call that writes the answer, and that is all.
//
// Two — a steer sent when no request is left to carry it was never applied. A
// run is a loop the caller's program drives; nothing outside a pattern can
// make it go round again. So the steer comes back on Result.UnappliedSteers,
// and one sent after the run is over is refused with ErrRunFinished.
//
// That second half is the honest cost of steering no pattern implements. It
// would be worse hidden: a steer that silently vanished is indistinguishable
// from one the model read and chose to ignore.
//
//	go run ./examples/steering-limits
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
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

	if err := plannedUpFront(ctx); err != nil {
		return err
	}
	return tooLate(ctx)
}

// plannedUpFront steers a pattern that is not ReAct and shares no code with
// it, which is the point: no pattern implements steering. The runner wraps the
// model client, so every pattern is steerable — including one written outside
// this module.
func plannedUpFront(ctx context.Context) error {
	fmt.Println("── ReWOO: the plan is already fixed ──────────────────────")

	endpoint, shutdown := gateway.Resolve(
		// One call decides the whole plan, before any tool runs.
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

	// The steer is sent from inside the tool — during execution, which is
	// after the plan exists and before the answer is written.
	handle := make(chan *agent.Run, 1)

	// Counted rather than claimed: the plan named one step, and a steer
	// arriving mid-execution cannot add or remove one.
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

// tooLate steers during what turns out to be the final request, and again
// after the run is over. Neither can be applied, and neither is swallowed.
func tooLate(ctx context.Context) error {
	fmt.Println("── A steer with no request left to carry it ──────────────")

	endpoint, shutdown := gateway.Resolve(
		gateway.Answer("Nothing needed looking up."),
	)
	defer shutdown()

	// The steer is sent from inside the model client, on the last call — by
	// which point the runner has already decided what that request carries.
	// Contrived, and it is the only way to hit the case on purpose: in a real
	// program it is somebody typing a half-second too late.
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

	// And once the run is over there is nowhere for it to go at all.
	err = run.Steer("later still")
	fmt.Printf("  sent after the run          %v\n", err)
	if !errors.Is(err, agent.ErrRunFinished) {
		return fmt.Errorf("expected ErrRunFinished, got: %w", err)
	}

	fmt.Println()
	gateway.Summary(result)
	return nil
}

// steeringTool steers from inside a tool, which for ReWOO is during execution
// — the window between the plan and the answer.
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

// lateSteerer steers from inside the model client, after the runner has
// already assembled the request it is being handed.
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
