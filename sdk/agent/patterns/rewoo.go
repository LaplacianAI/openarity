package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
)

func ReWOO() agent.Pattern { return rewoo{} }

func ReWOOStreaming() agent.Pattern { return rewoo{stream: true} }

type rewoo struct{ stream bool }

func (rewoo) Name() agent.PatternName { return agent.PatternReWOO }

const PlanToolName = "submit_plan"

var (
	ErrNoPlan    = errors.New("the planning call did not submit a plan")
	ErrEmptyPlan = errors.New("the plan has no steps")
)

func (l rewoo) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if in.Spec.MaxSteps <= 0 {
		return agent.Result{}, agent.ErrNoMaxSteps
	}
	if len(in.Spec.Tools) == 0 {
		return agent.Result{}, errors.New("the spec offers no tools, so there is nothing to plan with")
	}

	result := agent.Result{Messages: slices.Clone(in.Messages)}
	in.Emit(ctx, agent.StepEvent{Step: 1})
	plan, call, resp, err := l.plan(ctx, in)
	result.Steps = 1
	if err != nil {
		return result, err
	}

	msgs := append(slices.Clone(in.Messages), resp.Message)

	evidence, err := l.execute(ctx, in, plan)
	if err != nil {
		result.Messages = msgs
		return result, err
	}

	msgs = append(msgs, agent.Message{
		Role:       agent.RoleTool,
		ToolCallID: call.ID,
		Content:    []agent.Content{{Type: agent.ContentText, Text: evidence}},
	})

	in.Emit(ctx, agent.StepEvent{Step: 2})
	answer, err := l.solve(ctx, in, msgs)
	result.Steps = 2
	if err != nil {
		result.Messages = msgs
		return result, err
	}

	msgs = append(msgs, answer.Message)
	result.Messages = msgs
	result.Output = answer.Message.Text()

	if answer.Finish == agent.FinishLength {
		return result, agent.ErrTruncated
	}
	return result, nil
}

type step struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	Why  string          `json:"why"`
}

func (l rewoo) plan(ctx context.Context, in agent.Input) ([]step, agent.ToolCall, agent.Response, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), catalogue(in.Spec.Tools, in.Spec.MaxSteps))
	ask.Spec.Tools = []agent.Tool{planTool(in.Spec.Tools)}

	resp, err := (rewoo{}).call(ctx, ask, in.Messages)
	if err != nil {
		return nil, agent.ToolCall{}, resp, fmt.Errorf("planning: %w", err)
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})

	if resp.Finish == agent.FinishLength {
		return nil, agent.ToolCall{}, resp, agent.ErrTruncated
	}

	var call agent.ToolCall
	for _, c := range resp.Message.ToolCalls {
		if c.Name == PlanToolName {
			call = c
			break
		}
	}
	if call.Name == "" {
		return nil, call, resp, ErrNoPlan
	}

	var submitted struct {
		Steps []step `json:"steps"`
	}
	if err := json.Unmarshal(call.Arguments, &submitted); err != nil {
		return nil, call, resp, fmt.Errorf("%w: its arguments were not valid JSON: %w", ErrNoPlan, err)
	}
	if len(submitted.Steps) == 0 {
		return nil, call, resp, ErrEmptyPlan
	}
	if n := len(submitted.Steps); n > in.Spec.MaxSteps {
		return nil, call, resp, fmt.Errorf("%w: the plan asks for %d steps against a ceiling of %d",
			agent.ErrMaxSteps, n, in.Spec.MaxSteps)
	}

	return submitted.Steps, call, resp, nil
}

func (l rewoo) execute(ctx context.Context, in agent.Input, plan []step) (string, error) {
	byName := make(map[string]agent.Tool, len(in.Spec.Tools))
	for _, t := range in.Spec.Tools {
		byName[t.Name] = t
	}

	var (
		b        strings.Builder
		evidence = make([]string, 0, len(plan))
	)
	b.WriteString("Evidence gathered by the plan:\n")

	for i, s := range plan {
		id := "#E" + strconv.Itoa(i+1)

		args, err := substitute(s.Args, evidence)
		if err != nil {
			out := err.Error()
			evidence = append(evidence, out)
			record(&b, id, s.Tool, s.Args, out)
			continue
		}

		call := agent.ToolCall{ID: id, Name: s.Tool, Arguments: args}
		in.Emit(ctx, agent.ToolCallEvent{ID: id, Name: s.Tool, Arguments: args})

		started := time.Now()
		out, failure := invoke(ctx, byName, call)

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		in.Emit(ctx, agent.ToolResultEvent{
			ID: id, Name: s.Tool, Output: out,
			Err: failure, Duration: time.Since(started),
		})

		evidence = append(evidence, out)
		record(&b, id, s.Tool, args, out)
	}

	return b.String(), nil
}

func record(b *strings.Builder, id, name string, args json.RawMessage, out string) {
	fmt.Fprintf(b, "%s = %s(%s)\n  %s\n", id, name, args, out)
}

func (l rewoo) solve(ctx context.Context, in agent.Input, msgs []agent.Message) (agent.Response, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), solving)
	ask.Spec.Tools = nil

	resp, err := (rewoo{stream: l.stream}).call(ctx, ask, msgs)
	if err != nil {
		return resp, fmt.Errorf("solving: %w", err)
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})
	return resp, nil
}

func (l rewoo) call(ctx context.Context, in agent.Input, msgs []agent.Message) (agent.Response, error) {
	return react{stream: l.stream}.call(ctx, in, msgs)
}

func substitute(args json.RawMessage, evidence []string) (json.RawMessage, error) {
	if len(args) == 0 {
		return json.RawMessage(`{}`), nil
	}

	var doc map[string]any
	if err := json.Unmarshal(args, &doc); err != nil {
		return nil, fmt.Errorf("the arguments are not an object: %w", err)
	}

	replaced, err := walk(doc, evidence)
	if err != nil {
		return nil, err
	}

	out, _ := json.Marshal(replaced)
	return out, nil
}

func walk(v any, evidence []string) (any, error) {
	switch t := v.(type) {
	case string:
		return fill(t, evidence)
	case []any:
		for i, e := range t {
			out, err := walk(e, evidence)
			if err != nil {
				return nil, err
			}
			t[i] = out
		}
		return t, nil
	case map[string]any:
		for k, e := range t {
			out, err := walk(e, evidence)
			if err != nil {
				return nil, err
			}
			t[k] = out
		}
		return t, nil
	default:
		return v, nil
	}
}

func fill(s string, evidence []string) (string, error) {
	for i := len(evidence); i >= 1; i-- {
		s = strings.ReplaceAll(s, "#E"+strconv.Itoa(i), evidence[i-1])
	}

	if at := strings.Index(s, "#E"); at >= 0 {
		rest := s[at+2:]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end > 0 {
			return "", fmt.Errorf("this step refers to #E%s, which had not run when it was needed", rest[:end])
		}
	}
	return s, nil
}

func planTool(tools []agent.Tool) agent.Tool {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"steps": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "The tool calls to make, in order. Refer to an earlier step's output as #E1, #E2 and so on.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool": map[string]any{"type": "string", "enum": names},
						"args": map[string]any{"type": "object"},
						"why":  map[string]any{"type": "string", "description": "One line on what this step is for."},
					},
					"required":             []string{"tool", "args", "why"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"steps"},
		"additionalProperties": false,
	})

	return agent.Tool{
		Name:        PlanToolName,
		Description: "Submit the complete plan of tool calls for this task. Call this once, with every step.",
		Schema:      json.RawMessage(schema),
		Invoke: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("the plan is read by the pattern, not invoked")
		},
	}
}

func catalogue(tools []agent.Tool, maxSteps int) agent.Content {
	var b strings.Builder
	b.WriteString("Plan the whole task before anything runs. You will not see any result until every step is done, so no step may depend on judging an earlier one — only on substituting its output.\n\n")
	fmt.Fprintf(&b, "Use at most %d steps. The tools available are:\n\n", maxSteps)

	for _, t := range tools {
		fmt.Fprintf(&b, "- %s: %s\n  arguments: %s\n", t.Name, t.Description, t.Schema)
	}

	b.WriteString("\nWrite #E1, #E2 and so on where a step needs an earlier step's output. A step may only refer to steps before it.\n")
	b.WriteString("Then call " + PlanToolName + " once with every step.\n")

	return agent.Content{Type: agent.ContentText, Text: b.String()}
}

var solving = agent.Content{
	Type: agent.ContentText,
	Text: "The plan has run. Answer the question from the evidence above. Where a step failed, say what could not be determined rather than guessing.",
}
