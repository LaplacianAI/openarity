package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func ReAct() agent.Pattern { return react{} }

func ReActStreaming() agent.Pattern { return react{stream: true} }

// aside marks a call a pattern makes for itself, a plan or a critique: what
// the model writes there is reasoning, not said to the person waiting.
type react struct{ stream, aside bool }

func (react) Name() agent.PatternName { return agent.PatternReAct }

func (l react) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if in.Spec.MaxSteps <= 0 {
		return agent.Result{}, agent.ErrNoMaxSteps
	}

	byName := make(map[string]agent.Tool, len(in.Spec.Tools))
	for _, t := range in.Spec.Tools {
		byName[t.Name] = t
	}

	msgs := slices.Clone(in.Messages)
	result := agent.Result{Messages: msgs}

	// A run resumed in the middle of a turn, after a tool suspended it,
	// finishes that turn before the model is asked anything: a provider
	// refuses a conversation with a call left unanswered.
	if open, ok := openTurn(msgs); ok {
		var err error
		msgs, err = finishTurn(ctx, in, byName, msgs, open)
		result.Messages = msgs
		if err != nil {
			return result, err
		}
	}

	for step := 1; step <= in.Spec.MaxSteps; step++ {
		in.Emit(ctx, agent.StepEvent{Step: step})

		resp, err := l.call(ctx, in, msgs)
		if err != nil {
			return result, fmt.Errorf("step %d: %w", step, err)
		}

		result.Steps = step
		in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

		c, err := carryOn(ctx, in, msgs, resp, l.call)
		msgs = append(msgs, c.msgs...)
		result.Messages = msgs
		result.Output = c.text
		if err != nil {
			return result, fmt.Errorf("step %d: %w", step, err)
		}
		resp = c.last

		// A call cut off part-way is never run: it is answered, and the model
		// tries again with less in it.
		if resp.Finish == agent.FinishLength {
			msgs = append(msgs, answerCut(&msgs[len(msgs)-1])...)
			result.Messages = msgs
			continue
		}
		if len(resp.Message.ToolCalls) == 0 {
			return result, nil
		}

		results, attached, err := dispatch(ctx, in, byName, resp.Message.ToolCalls)
		msgs = append(msgs, results...)
		// After every result: a provider wants each call answered before
		// anything else is said. A suspended turn keeps what its earlier
		// calls attached here too, and a resume puts the rest before it.
		if ctx.Err() == nil {
			if files, ok := agent.Attached(attached, resp.Message.ToolCalls); ok {
				msgs = append(msgs, files)
			}
		}
		result.Messages = msgs
		if err != nil {
			return result, err
		}
	}

	return result, agent.ErrMaxSteps
}

func (l react) call(ctx context.Context, in agent.Input, msgs []agent.Message) (agent.Response, error) {
	req := agent.Request{
		Model:    in.Spec.Model,
		System:   in.Spec.System,
		Messages: msgs,
		Tools:    in.Spec.Tools,
	}

	if !l.stream {
		resp, err := in.Model.Complete(ctx, req)
		if err != nil {
			return agent.Response{}, err
		}
		if r := resp.Message.Reasoning; r != nil && r.Text != "" {
			in.Emit(ctx, agent.ReasoningEvent{Delta: r.Text})
		}
		l.say(ctx, in, resp.Message.Text())
		return resp, nil
	}

	s, err := in.Model.Stream(ctx, req)
	if err != nil {
		return agent.Response{}, err
	}
	defer s.Close()

	var final *agent.Response
	for s.Next() {
		ev := s.Event()
		if ev.Reasoning != "" {
			in.Emit(ctx, agent.ReasoningEvent{Delta: ev.Reasoning})
		}
		l.say(ctx, in, ev.Text)
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if err := s.Err(); err != nil {
		return agent.Response{}, err
	}
	if final == nil {
		return agent.Response{}, agent.ErrIncompleteStream
	}
	return *final, nil
}

// say emits what the model wrote: as text, or as reasoning in a call aside.
func (l react) say(ctx context.Context, in agent.Input, text string) {
	switch {
	case text == "":
	case l.aside:
		in.Emit(ctx, agent.ReasoningEvent{Delta: text})
	default:
		in.Emit(ctx, agent.TextEvent{Delta: text})
	}
}

// dispatch makes calls in order, as one turn's. It stops at a cancelled
// context, before the result of the call it interrupted, and at a call whose
// tool suspended the run, which gets no result and leaves the calls after it
// unmade; either way the error says which.
func dispatch(ctx context.Context, in agent.Input, byName map[string]agent.Tool, calls []agent.ToolCall,
) ([]agent.Message, map[string][]agent.Content, error) {
	var results []agent.Message
	attached := map[string][]agent.Content{}
	for _, call := range calls {
		// Events name the tool the model meant, not the call_tool it went through.
		shown := agent.Inner(call)
		in.Emit(ctx, agent.ToolCallEvent{ID: call.ID, Name: shown.Name, Arguments: shown.Arguments})

		started := time.Now()
		out, failure, attach, suspended := invoke(ctx, byName, call)

		if ctx.Err() != nil {
			return results, attached, ctx.Err()
		}
		if suspended != nil {
			return results, attached, suspended
		}
		attached[call.ID] = attach

		in.Emit(ctx, agent.ToolResultEvent{
			ID: call.ID, Name: shown.Name, Output: out,
			Err: failure, Duration: time.Since(started),
		})

		results = append(results, agent.Message{
			Role:       agent.RoleTool,
			ToolCallID: call.ID,
			Content:    []agent.Content{{Type: agent.ContentText, Text: out}},
		})
	}
	return results, attached, nil
}

// invoke makes one call. A tool's failure is text the model reads; a
// suspension is not a failure, and comes back as an error of its own so that
// no pattern turns it into a result.
func invoke(ctx context.Context, byName map[string]agent.Tool, call agent.ToolCall,
) (out, failure string, attach []agent.Content, suspended error) {
	t, ok := byName[call.Name]
	if !ok {
		return fmt.Sprintf("there is no tool named %q available in this step", call.Name), "unknown tool", nil, nil
	}
	if !json.Valid(call.Arguments) {
		return "the arguments were not valid JSON; call the tool again with corrected arguments", "invalid arguments", nil, nil
	}

	result, callable, err := t.Call(agent.WithToolCallID(ctx, call.ID), call.Arguments)
	if !callable {
		return fmt.Sprintf("the tool %q cannot be called here", call.Name), "tool has no implementation", nil, nil
	}
	if errors.Is(err, agent.ErrSuspended) {
		return "", "", nil, err
	}
	if failure := failureOf(err); failure != "" {
		return failure, failure, nil, nil
	}
	return result.Output, "", result.Attach, nil
}

// failureOf is a tool's error as the model reads it; empty for none.
func failureOf(err error) string {
	if err == nil {
		return ""
	}
	if s := err.Error(); s != "" {
		return s
	}
	return "the tool failed and said nothing about it"
}
