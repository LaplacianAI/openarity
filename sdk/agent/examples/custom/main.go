// Command custom implements a loop of its own, outside the SDK.
//
// The pattern is plan-then-act: one cheap call with no tools to get a plan,
// then the shipped ReAct loop to carry it out. It is here to show two things
// a deployment needs and neither of which requires touching sdk/agent — that
// agent.Loop is an ordinary interface anyone can satisfy, and that a custom
// loop can delegate to a shipped one rather than reimplementing it.
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
	"github.com/LaplacianAI/openarity/sdk/agent/loops"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

// LoopPlanAct is this deployment's own name for its own pattern. A LoopType is
// a string, so nothing in the SDK has to learn about it — the brain puts this
// in Spec.Loop and the runner finds it in the registry like any other.
const LoopPlanAct agent.LoopType = "plan-act"

type planAct struct{}

func (planAct) Name() agent.LoopType { return LoopPlanAct }

func (l planAct) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	// The same refusals every loop makes. MaxSteps is two here rather than one
	// because the plan spends a step before any work happens, and a ceiling of
	// one would buy a plan and nothing to carry it out.
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if in.Spec.MaxSteps < 2 {
		return agent.Result{}, fmt.Errorf("%w: plan-act needs at least two", agent.ErrNoMaxSteps)
	}

	in.Emit(ctx, agent.StepEvent{Step: 1})

	// Phase one: no tools on the request at all. The model cannot act even if
	// it wants to, which is what makes this a plan rather than a first step.
	plan, err := in.Model.Complete(ctx, agent.Request{
		Model:    in.Spec.Model,
		System:   append(in.Spec.System, planning),
		Messages: in.Messages,
	})
	if err != nil {
		return agent.Result{}, fmt.Errorf("planning: %w", err)
	}
	// Emitted the way the shipped loops emit it. A phase that spends a step and
	// a model call while showing nothing reads as a stall to whoever is
	// watching the run.
	if text := plan.Message.Text(); text != "" {
		in.Emit(ctx, agent.TextEvent{Delta: text})
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: plan.Usage})

	// The plan joins the transcript as the assistant's own words, so phase two
	// reads it as something it said rather than something it was told.
	msgs := append(append([]agent.Message{}, in.Messages...), plan.Message)

	// Phase two is the shipped loop, unchanged. Its ceiling is what is left
	// after the plan, because MaxSteps is the caller's whole budget for the
	// run and not a per-phase allowance.
	spec := in.Spec
	spec.MaxSteps--

	events, drain := renumber(ctx, in.Events, 1)
	result, err := loops.ReAct().Run(ctx, agent.Input{
		Spec:     spec,
		Messages: msgs,
		Model:    in.Model,
		Events:   events,
	})
	drain()

	// The plan's step and tokens are this loop's to report. ReAct counted only
	// what it did, and a caller reading Steps should see the whole run.
	result.Steps++
	result.Usage.InputTokens += plan.Usage.InputTokens
	result.Usage.OutputTokens += plan.Usage.OutputTokens
	result.Usage.CachedInputTokens += plan.Usage.CachedInputTokens

	return result, err
}

// planning is appended to whatever system prompt the brain resolved, rather
// than replacing it. A loop that overwrote it would drop the agent's identity
// and every guideline the deployment set.
var planning = agent.Content{
	Type: agent.ContentText,
	Text: "First, write a short numbered plan for what you will do. Do not do it yet.",
}

// renumber forwards an inner loop's events with its step numbers shifted, so a
// run that spent one step planning and two acting reads as 1, 2, 3 rather than
// 1, then 1 again. Without it the two loops both start counting at one and the
// transcript looks like a retry.
func renumber(ctx context.Context, out chan<- agent.Event, offset int) (chan agent.Event, func()) {
	if out == nil {
		return nil, func() {}
	}

	in := make(chan agent.Event, 64)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for e := range in {
			if s, ok := e.(agent.StepEvent); ok {
				e = agent.StepEvent{Step: s.Step + offset}
			}
			// The same bargain agent.Input.Emit makes: a consumer that stopped
			// reading must not be able to wedge a loop mid-run.
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Closing is the caller's job and must happen before the inner result is
	// used, or the last events race the summary that is printed after them.
	return in, func() { close(in); <-done }
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

	// Registered alongside the shipped loops, by name. New refuses two loops
	// claiming one name and says which two, so a clash is a startup error
	// rather than whichever the map happened to hold.
	runner, err := agent.New(openaicompat.Factory(), loops.ReAct(), planAct{})
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Loop:     LoopPlanAct,
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
		gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	if err != nil {
		return err
	}
	gateway.Summary(result)
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
