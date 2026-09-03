package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func Reflection(cycles int) agent.Pattern { return reflection{cycles: cycles} }

func ReflectionStreaming(cycles int) agent.Pattern {
	return reflection{cycles: cycles, stream: true}
}

type reflection struct {
	stream bool
	cycles int
}

func (reflection) Name() agent.PatternName { return agent.PatternReflection }

const CritiqueToolName = "submit_critique"

var (
	ErrNoCycles   = errors.New("the pattern was built with no reflection cycles, so it would never reflect")
	ErrNoCritique = errors.New("the reflecting call did not submit a critique")
)

func (l reflection) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if l.cycles <= 0 {
		return agent.Result{}, ErrNoCycles
	}

	needed := 1 + 2*l.cycles
	if in.Spec.MaxSteps < needed {
		return agent.Result{}, fmt.Errorf(
			"%w: this pattern answers and then critiques and rewrites %d time(s), so it needs at least %d",
			agent.ErrNoMaxSteps, l.cycles, needed)
	}

	spec := in.Spec
	spec.MaxSteps = in.Spec.MaxSteps - 2*l.cycles

	result, err := react{stream: l.stream}.Run(ctx, agent.Input{
		Spec:     spec,
		Messages: in.Messages,
		Model:    in.Model,
		Events:   in.Events,
	})
	if err != nil {
		return result, fmt.Errorf("generating: %w", err)
	}

	msgs := result.Messages
	step := result.Steps

	for cycle := 1; cycle <= l.cycles; cycle++ {
		step++
		in.Emit(ctx, agent.StepEvent{Step: step})
		result.Steps = step

		verdict, err := l.reflect(ctx, in, msgs)
		if err != nil {
			return result, fmt.Errorf("reflecting on cycle %d: %w", cycle, err)
		}
		if !verdict.NeedsRefinement {
			return result, nil
		}

		step++
		in.Emit(ctx, agent.StepEvent{Step: step})
		result.Steps = step

		improved, err := l.refine(ctx, in, msgs, verdict.Critique)
		if err != nil {
			return result, fmt.Errorf("rewriting on cycle %d: %w", cycle, err)
		}

		msgs = append(slices.Clone(msgs[:len(msgs)-1]), improved.Message)
		result.Messages = msgs
		result.Output = improved.Message.Text()
	}

	return result, nil
}

type critique struct {
	NeedsRefinement bool   `json:"needs_refinement"`
	Critique        string `json:"critique"`
}

func (l reflection) reflect(ctx context.Context, in agent.Input, msgs []agent.Message) (critique, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), reflecting)
	ask.Spec.Tools = []agent.Tool{critiqueTool()}

	resp, err := (reflection{}).call(ctx, ask, msgs)
	if err != nil {
		return critique{}, err
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	if resp.Finish == agent.FinishLength {
		return critique{}, agent.ErrTruncated
	}

	var call agent.ToolCall
	for _, c := range resp.Message.ToolCalls {
		if c.Name == CritiqueToolName {
			call = c
			break
		}
	}
	if call.Name == "" {
		return critique{}, ErrNoCritique
	}

	var verdict critique
	if err := json.Unmarshal(call.Arguments, &verdict); err != nil {
		return critique{}, fmt.Errorf("%w: its arguments were not valid JSON: %w", ErrNoCritique, err)
	}
	if verdict.NeedsRefinement && verdict.Critique == "" {
		return critique{}, fmt.Errorf("%w: it asked for changes without saying which", ErrNoCritique)
	}
	return verdict, nil
}

func (l reflection) refine(
	ctx context.Context, in agent.Input, msgs []agent.Message, note string,
) (agent.Response, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), refining)
	ask.Spec.Tools = nil

	asked := append(slices.Clone(msgs), agent.Message{
		Role: agent.RoleUser,
		Content: []agent.Content{{
			Type: agent.ContentText,
			Text: "A reviewer read your last answer and said:\n\n" + note +
				"\n\nRewrite the answer so it addresses this. Reply with the improved answer alone.",
		}},
	})

	resp, err := (reflection{stream: l.stream}).call(ctx, ask, asked)
	if err != nil {
		return agent.Response{}, err
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	if resp.Finish == agent.FinishLength {
		return agent.Response{}, agent.ErrTruncated
	}
	return resp, nil
}

func (l reflection) call(ctx context.Context, in agent.Input, msgs []agent.Message) (agent.Response, error) {
	return react{stream: l.stream}.call(ctx, in, msgs)
}

func critiqueTool() agent.Tool {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"needs_refinement": map[string]any{
				"type": "boolean",
				"description": "True only if rewriting would make the answer materially better. " +
					"An answer that is already correct and complete does not need changing.",
			},
			"critique": map[string]any{
				"type":        "string",
				"description": "What is wrong and what would fix it. Empty when nothing needs changing.",
			},
		},
		"required":             []string{"needs_refinement", "critique"},
		"additionalProperties": false,
	})

	return agent.Tool{
		Name: CritiqueToolName,
		Description: "Submit your judgement of the answer above. Call this once, " +
			"whether or not you think anything should change.",
		Schema: json.RawMessage(schema),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("the critique is read by the pattern, not invoked")
		},
	}
}

var reflecting = agent.Content{
	Type: agent.ContentText,
	Text: "Judge the assistant's last answer against what was asked. Look for claims that are " +
		"wrong, requirements that went unanswered, and reasoning that does not hold. Being " +
		"agreeable is not useful here, and neither is inventing faults in an answer that is " +
		"already right. Then call " + CritiqueToolName + " once.",
}

var refining = agent.Content{
	Type: agent.ContentText,
	Text: "Rewrite your previous answer to address the critique. Keep what was already right; " +
		"change only what the critique identifies. Do not mention the critique or the rewrite.",
}
