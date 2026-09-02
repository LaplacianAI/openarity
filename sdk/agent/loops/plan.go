package loops

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func Plan() agent.Loop { return plan{} }

func PlanStreaming() agent.Loop { return plan{stream: true} }

type plan struct{ stream bool }

func (plan) Name() agent.LoopType { return agent.LoopPlan }

var ErrPlanCalledATool = errors.New("the planning turn called a tool, but the planning request offered none")

var planning = agent.Content{
	Type: agent.ContentText,
	Text: "First, write a short numbered plan for what you will do. Do not do it yet.",
}

func (l plan) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if in.Spec.MaxSteps < 2 {
		return agent.Result{}, fmt.Errorf("%w: this loop plans before it acts, so it needs at least two", agent.ErrNoMaxSteps)
	}

	in.Emit(ctx, agent.StepEvent{Step: 1})

	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), planning)
	ask.Spec.Tools = nil

	resp, err := react{stream: l.stream}.call(ctx, ask, in.Messages)
	if err != nil {
		return agent.Result{}, fmt.Errorf("planning: %w", err)
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	if resp.Finish == agent.FinishLength {
		return agent.Result{
			Messages: append(slices.Clone(in.Messages), resp.Message),
			Output:   resp.Message.Text(),
			Usage:    resp.Usage,
			Steps:    1,
		}, agent.ErrTruncated
	}
	if len(resp.Message.ToolCalls) > 0 {
		return agent.Result{Steps: 1, Usage: resp.Usage}, ErrPlanCalledATool
	}

	msgs := append(slices.Clone(in.Messages), resp.Message)

	spec := in.Spec
	spec.MaxSteps--

	events, drain := renumber(ctx, in.Events, 1)
	result, err := react{stream: l.stream}.Run(ctx, agent.Input{
		Spec:     spec,
		Messages: msgs,
		Model:    in.Model,
		Events:   events,
	})
	drain()

	result.Steps++
	result.Usage.InputTokens += resp.Usage.InputTokens
	result.Usage.OutputTokens += resp.Usage.OutputTokens
	result.Usage.CachedInputTokens += resp.Usage.CachedInputTokens

	return result, err
}

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

			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()

	return in, func() { close(in); <-done }
}
