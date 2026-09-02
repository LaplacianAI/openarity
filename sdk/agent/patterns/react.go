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

type react struct{ stream bool }

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

	for step := 1; step <= in.Spec.MaxSteps; step++ {
		in.Emit(ctx, agent.StepEvent{Step: step})

		resp, err := l.call(ctx, in, msgs)
		if err != nil {
			return result, fmt.Errorf("step %d: %w", step, err)
		}

		result.Steps = step
		result.Usage.InputTokens += resp.Usage.InputTokens
		result.Usage.OutputTokens += resp.Usage.OutputTokens
		result.Usage.CachedInputTokens += resp.Usage.CachedInputTokens
		in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

		msgs = append(msgs, resp.Message)
		result.Messages = msgs
		result.Output = resp.Message.Text()

		if resp.Finish == agent.FinishLength {
			return result, agent.ErrTruncated
		}
		if len(resp.Message.ToolCalls) == 0 {
			return result, nil
		}

		for _, call := range resp.Message.ToolCalls {
			in.Emit(ctx, agent.ToolCallEvent{ID: call.ID, Name: call.Name, Arguments: call.Arguments})

			started := time.Now()
			out, failure := invoke(ctx, byName, call)

			if ctx.Err() != nil {
				return result, ctx.Err()
			}

			in.Emit(ctx, agent.ToolResultEvent{
				ID: call.ID, Name: call.Name, Output: out,
				Err: failure, Duration: time.Since(started),
			})

			msgs = append(msgs, agent.Message{
				Role:       agent.RoleTool,
				ToolCallID: call.ID,
				Content:    []agent.Content{{Type: agent.ContentText, Text: out}},
			})
			result.Messages = msgs
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
		if text := resp.Message.Text(); text != "" {
			in.Emit(ctx, agent.TextEvent{Delta: text})
		}
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
		if ev.Text != "" {
			in.Emit(ctx, agent.TextEvent{Delta: ev.Text})
		}
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

func invoke(ctx context.Context, byName map[string]agent.Tool, call agent.ToolCall) (out, failure string) {
	t, ok := byName[call.Name]
	if !ok {
		return fmt.Sprintf("there is no tool named %q available in this step", call.Name), "unknown tool"
	}
	if !json.Valid(call.Arguments) {
		return "the arguments were not valid JSON; call the tool again with corrected arguments", "invalid arguments"
	}
	if t.Invoke == nil {
		return fmt.Sprintf("the tool %q cannot be called here", call.Name), "tool has no implementation"
	}

	result, err := t.Invoke(ctx, call.Arguments)
	if err != nil {
		return err.Error(), err.Error()
	}
	return result, ""
}
