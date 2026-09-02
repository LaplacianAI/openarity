package loops

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Plan runs plan-then-act: one call with no tools to get a plan, then ReAct to
// carry it out.
//
// The plan is worth its step when a task needs several tools in an order the
// model has to work out. It is a step wasted when the task is one tool call,
// which is why this is a loop the brain chooses rather than something ReAct
// does on its own.
func Plan() agent.Loop { return plan{} }

// PlanStreaming is Plan with both phases streamed, so the plan appears as it is
// written rather than after it is finished.
func PlanStreaming() agent.Loop { return plan{stream: true} }

type plan struct{ stream bool }

func (plan) Name() agent.LoopType { return agent.LoopPlan }

// ErrPlanCalledATool is returned when the planning turn comes back with tool
// calls. The request carried no tools, so a gateway that produced one ignored
// the field — and appending that turn would leave a tool call nothing answers,
// which the next request is rejected for.
var ErrPlanCalledATool = errors.New("the planning turn called a tool, but the planning request offered none")

// planning is appended to the spec's system prompt rather than replacing it. A
// loop that overwrote it would drop the agent's identity and every guideline
// the deployment set, and nothing would fail visibly when it did.
var planning = agent.Content{
	Type: agent.ContentText,
	Text: "First, write a short numbered plan for what you will do. Do not do it yet.",
}

func (l plan) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	// Two rather than one: the plan spends a step before any work happens, and
	// a ceiling of one buys a plan with nothing left to carry it out.
	if in.Spec.MaxSteps < 2 {
		return agent.Result{}, fmt.Errorf("%w: this loop plans before it acts, so it needs at least two", agent.ErrNoMaxSteps)
	}

	in.Emit(ctx, agent.StepEvent{Step: 1})

	// The same call ReAct makes, through the same code, so the plan streams
	// when the run streams and every stream failure is reported identically.
	// Only the request differs: the system prompt gains a line and the tools
	// are removed, so the model cannot act even if it wants to.
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), planning)
	ask.Spec.Tools = nil

	resp, err := react{stream: l.stream}.call(ctx, ask, in.Messages)
	if err != nil {
		return agent.Result{}, fmt.Errorf("planning: %w", err)
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	// A plan cut off at MaxTokens is half a plan, and the phase that follows
	// would work from it without knowing.
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

	// The plan joins the transcript as the assistant's own words, so the second
	// phase reads it as something it said rather than something it was told.
	msgs := append(slices.Clone(in.Messages), resp.Message)

	// MaxSteps is the caller's whole budget for the run, not a per-phase
	// allowance. Resetting it here would make this loop cost twice what the
	// brain asked for.
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

	// The plan's step and tokens belong to this loop. ReAct counted only what
	// it did, and a caller reading Steps should see the whole run.
	result.Steps++
	result.Usage.InputTokens += resp.Usage.InputTokens
	result.Usage.OutputTokens += resp.Usage.OutputTokens
	result.Usage.CachedInputTokens += resp.Usage.CachedInputTokens

	return result, err
}

// renumber forwards an inner loop's events with its step numbers shifted, so a
// run that spent one step planning and two acting reads as 1, 2, 3 rather than
// 1, then 1 again. Without it the two loops both start counting at one and the
// transcript looks like a retry.
//
// The returned function must be called before the result is used, or the last
// events race whatever the caller prints after them.
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

	return in, func() { close(in); <-done }
}
