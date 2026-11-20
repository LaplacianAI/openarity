package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

// Reflection answers, then reviews and rewrites its answer up to cycles
// times. A cycle is two steps, a review and a rewrite; a review the model
// writes out rather than submits is asked for once more inside its step, so
// a step may make two calls.
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
			return kept(ctx, in, result, step, "critique", fmt.Errorf("reflecting on cycle %d: %w", cycle, err))
		}
		if !verdict.NeedsRefinement {
			return result, nil
		}

		step++
		in.Emit(ctx, agent.StepEvent{Step: step})
		result.Steps = step

		improved, err := l.refine(ctx, in, msgs, verdict.Critique)
		if err != nil {
			return kept(ctx, in, result, step, "rewrite", fmt.Errorf("rewriting on cycle %d: %w", cycle, err))
		}

		msgs = append(slices.Clone(msgs[:answerStart(msgs)]), improved.msgs...)
		result.Messages = msgs
		result.Output = improved.text
	}

	return result, nil
}

// kept is the answer a review or a rewrite was working on, handed back when
// that work failed: a finished answer is never lost to a pattern's own step.
// Only a cancelled run stops, since nobody is waiting for its answer.
func kept(ctx context.Context, in agent.Input, result agent.Result, step int, what string, err error) (agent.Result, error) {
	if ctx.Err() != nil {
		return result, err
	}
	in.Emit(ctx, agent.SkippedEvent{Step: step, What: what, Reason: err.Error()})
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
	review := react{aside: true, must: CritiqueToolName}

	call, resp, ok, err := reviewOnce(ctx, in, ask, review, msgs)
	if err != nil {
		return critique{}, err
	}
	if !ok {
		// A model that thinks will not be told which tool to call, so the
		// openaicompat client drops tool_choice and asks again without it, and
		// the model often writes its review out instead. Shown that review and
		// asked to submit it, it usually does. Only its text is handed back,
		// in a user turn after the draft: the reply's reasoning, any stray
		// call and an empty turn are left out, as a provider may refuse them.
		// Once, in the same step, and the verdict is read from the call,
		// never guessed from the prose.
		if err := ctx.Err(); err != nil {
			return critique{}, err
		}
		withReview := append(slices.Clone(msgs), submitAgain(resp.Message.Text()))
		if call, _, ok, err = reviewOnce(ctx, in, ask, review, withReview); err != nil {
			return critique{}, err
		}
		if !ok {
			return critique{}, fmt.Errorf("%w, though asked twice", ErrNoCritique)
		}
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

// reviewOnce makes one reviewing call and records what it used. A reply cut
// off at max_tokens is an error; otherwise it reports the critique the reply
// submitted, if it submitted one.
func reviewOnce(
	ctx context.Context, in, ask agent.Input, review react, msgs []agent.Message,
) (agent.ToolCall, agent.Response, bool, error) {
	resp, err := review.call(ctx, ask, msgs)
	if err != nil {
		return agent.ToolCall{}, resp, false, err
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})
	if resp.Finish == agent.FinishLength {
		return agent.ToolCall{}, resp, false, agent.ErrTruncated
	}
	for _, c := range resp.Message.ToolCalls {
		if c.Name == CritiqueToolName {
			return c, resp, true, nil
		}
	}
	return agent.ToolCall{}, resp, false, nil
}

// submitAgain is the turn after a review written out rather than submitted:
// the review, fenced off as the model's own words, then the ask. A reply that
// wrote nothing gets the ask alone.
func submitAgain(review string) agent.Message {
	text := submitNow
	if review = strings.TrimSpace(review); review != "" {
		text = "Your review of the answer was:\n\n<review>\n" + review + "\n</review>\n\n" + submitNow
	}
	return agent.Message{Role: agent.RoleUser, Content: []agent.Content{{Type: agent.ContentText, Text: text}}}
}

func (l reflection) refine(
	ctx context.Context, in agent.Input, msgs []agent.Message, note string,
) (carried, error) {
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
		return carried{}, err
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	c, err := carryOn(ctx, ask, asked, resp, reflection{stream: l.stream}.call)
	if err != nil {
		// A rewrite never finished is no answer; the one before it stands.
		return carried{}, err
	}
	return c, nil
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

// submitNow follows a reflecting turn that wrote its review rather than
// calling the tool.
const submitNow = "Submit that judgement now: call " + CritiqueToolName +
	" with it. Do not write it out again."

var refining = agent.Content{
	Type: agent.ContentText,
	Text: "Rewrite your previous answer to address the critique. Keep what was already right; " +
		"change only what the critique identifies. Do not mention the critique or the rewrite.",
}
