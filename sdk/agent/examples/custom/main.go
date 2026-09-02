// Command custom implements a pattern of its own, outside the SDK.
//
// The pattern here is a wrapper: it takes any other pattern and enforces this
// deployment's token ceiling on top of it. That is deliberately not something
// sdk/agent should ship — a spending rule belongs to whoever pays the bill,
// and it changes when they change their mind. What the SDK ships is the
// interface that lets you write one.
//
// A pattern from the literature, like plan-then-act, is a different matter:
// that one is patterns.Plan(), and this example uses it as the wrapped pattern.
//
//	go run ./examples/custom
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
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// ErrOverBudget is what this deployment returns when a run costs more than it
// is allowed to. A caller can tell it apart from a model failure, which matters
// because one is worth retrying and the other is not.
var ErrOverBudget = errors.New("the run passed its token ceiling")

// Budgeted wraps any pattern and stops the run once it has spent more than
// maxTokens. It keeps the wrapped pattern's name, so the brain still asks for
// "plan" or "react" and this deployment's policy applies underneath — nothing
// about the Spec has to know the wrapper is there.
func Budgeted(inner agent.Pattern, maxTokens int) agent.Pattern {
	return budgeted{inner: inner, max: maxTokens}
}

type budgeted struct {
	inner agent.Pattern
	max   int
}

func (b budgeted) Name() agent.PatternName { return b.inner.Name() }

func (b budgeted) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	// The ceiling is enforced by watching the run rather than by inspecting it
	// afterwards, because afterwards the tokens are already spent. UsageEvent
	// is emitted per step, so cancelling on the step that crosses the line
	// costs at most one step of overshoot.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	watched, spent, drain := watch(ctx, in.Events, b.max, stop)
	in.Events = watched

	result, err := b.inner.Run(ctx, in)
	drain()

	// Checked after the run rather than trusting the error, because a
	// cancelled loop reports context.Canceled and says nothing about why.
	if spent() > b.max {
		return result, fmt.Errorf("%w: %d tokens against a ceiling of %d",
			ErrOverBudget, spent(), b.max)
	}
	return result, err
}

// watch forwards events untouched while adding up what the run has spent, and
// calls stop the moment it passes the ceiling.
func watch(ctx context.Context, out chan<- agent.Event, max int, stop func()) (chan agent.Event, func() int, func()) {
	in := make(chan agent.Event, 64)
	done := make(chan struct{})
	total := make(chan int, 1)
	total <- 0

	go func() {
		defer close(done)
		spent := 0
		for e := range in {
			if u, ok := e.(agent.UsageEvent); ok {
				spent += u.Usage.InputTokens + u.Usage.OutputTokens
				<-total
				total <- spent
				if spent > max {
					fmt.Printf("\n  ! %d tokens spent against a ceiling of %d — stopping", spent, max)
					stop()
				}
			}
			if out == nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				// Keep draining rather than returning: the pattern is still
				// emitting, and a forwarder that stopped reading would wedge
				// the very run this is trying to end.
			}
		}
	}()

	spent := func() int {
		n := <-total
		total <- n
		return n
	}
	return in, spent, func() { close(in); <-done }
}

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
		gateway.Answer("1. Count the open issues in openarity.\n2. Report the number."),
		gateway.ToolCall("count_issues", `{"repo":"openarity"}`),
		gateway.Answer("openarity has 3 open issues."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	// The wrapper registers under the name it wraps. Ask for "plan" and this
	// deployment's ceiling comes with it.
	runner, err := agent.New(openaicompat.Factory(),
		patterns.ReAct(),
		Budgeted(patterns.PlanStreaming(), 150),
	)
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternPlan,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{countIssues()},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "How many open issues does openarity have?"}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	// An over-budget run is reported, not hidden: what it managed before the
	// ceiling is still worth printing, and so is the reason it stopped.
	if err != nil && !errors.Is(err, ErrOverBudget) {
		return err
	}
	gateway.Summary(result)
	if err != nil {
		fmt.Printf("stopped  %v\n", err)
	}
	return nil
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
				return "", fmt.Errorf("could not read the arguments: %w", err)
			}
			return fmt.Sprintf("%s has 3 open issues", in.Repo), nil
		},
	}
}
