package patterns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

// ErrNoPlan is a plan submitted that cannot be read. A planning turn that
// submits none, or one of no steps, is not an error: the model found nothing
// a tool is needed for, as a greeting has, and the run goes to the answer.
var ErrNoPlan = errors.New("the planning call did not submit a readable plan")

// ErrCannotWait is a step whose tool suspended the run. ReWOO plans every
// call before it makes one and keeps nothing between them, so it has no turn
// to resume, and the run ends instead. It does not match agent.ErrSuspended:
// a caller that parks suspended runs must not park one that cannot go on.
var ErrCannotWait = errors.New("a tool suspended the run, and a planned run cannot wait")

// ErrToolMarkup is an answer that ends in a tool call written out as text:
// the answer step has no tools, and a model that still wants one can write
// its call as markup, which is no answer to send anyone.
var ErrToolMarkup = errors.New("the answer was a tool call written as text, not an answer")

var errNothingToPlan = errors.New("the planning call found nothing to plan")

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
	plan, call, resp, err := l.readPlan(ctx, in)
	result.Steps = 1
	// A plan the model wrote that cannot be read, is too long or was cut off
	// is answered as no plan at all: what the person asked is still owed an
	// answer. A planning call that failed outright is the run's failure.
	if unusable(err) {
		in.Emit(ctx, agent.SkippedEvent{Step: 1, What: "plan", Reason: err.Error()})
		err = errNothingToPlan
	}
	if errors.Is(err, errNothingToPlan) {
		// Answered as after a plan, with no evidence: the planning turn's
		// own words were never shown, so they are not kept as the answer.
		in.Emit(ctx, agent.StepEvent{Step: 2})
		answer, err := l.solve(ctx, in, slices.Clone(in.Messages), unplanned)
		result.Steps = 2
		result.Messages = append(slices.Clone(in.Messages), answer.msgs...)
		result.Output = answer.text
		return result, err
	}
	if err != nil {
		return result, err
	}

	msgs := append(slices.Clone(in.Messages), resp.Message)

	evidence, files, err := l.execute(ctx, in, plan)
	if err != nil {
		result.Messages = msgs
		return result, err
	}

	msgs = append(msgs, agent.Message{
		Role:       agent.RoleTool,
		ToolCallID: call.ID,
		Content:    []agent.Content{{Type: agent.ContentText, Text: evidence}},
	})
	// What the steps attached reaches the solver after the evidence, as a
	// turn's files do in ReAct.
	if attached, ok := agent.Attached(files.byCall, files.order); ok {
		msgs = append(msgs, attached)
	}

	in.Emit(ctx, agent.StepEvent{Step: 2})
	answer, err := l.solve(ctx, in, msgs, solving)
	result.Steps = 2
	msgs = append(msgs, answer.msgs...)
	result.Messages = msgs
	result.Output = answer.text
	return result, err
}

func unusable(err error) bool {
	return errors.Is(err, ErrNoPlan) || errors.Is(err, agent.ErrMaxSteps) || errors.Is(err, agent.ErrTruncated)
}

type step struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	Why  string          `json:"why"`
}

// readPlan asks for the plan, and once more when it cannot be read: steps in
// the wrong shape are a slip a model rarely makes twice. The refused call is
// not sent back, as its arguments may not parse; the model is told what shape
// the plan takes, not the parser's words.
func (l rewoo) readPlan(ctx context.Context, in agent.Input) ([]step, agent.ToolCall, agent.Response, error) {
	plan, call, resp, err := l.plan(ctx, in)
	if !errors.Is(err, ErrNoPlan) {
		return plan, call, resp, err
	}
	again := in
	again.Messages = withNote(in.Messages, "Your plan could not be read. Call "+PlanToolName+
		" again with the whole plan: steps is a list of objects, each with tool, args and why.")
	return l.plan(ctx, again)
}

// withNote is msgs with text added for the model: to the last message when
// that is the person's, so two user turns never follow each other, else as a
// message of its own.
func withNote(msgs []agent.Message, text string) []agent.Message {
	out := slices.Clone(msgs)
	if n := len(out); n > 0 && out[n-1].Role == agent.RoleUser && !out[n-1].Attached {
		last := out[n-1]
		last.Content = append(slices.Clone(last.Content), agent.Content{Type: agent.ContentText, Text: text})
		out[n-1] = last
		return out
	}
	return append(out, userText(text))
}

func (l rewoo) plan(ctx context.Context, in agent.Input) ([]step, agent.ToolCall, agent.Response, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), catalogue(in.Spec.Tools, in.Spec.MaxSteps))
	ask.Spec.Tools = []agent.Tool{planTool(in.Spec.Tools)}

	resp, err := react{aside: true}.call(ctx, ask, in.Messages)
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
		return nil, call, resp, errNothingToPlan
	}

	var submitted struct {
		Steps json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(call.Arguments, &submitted); err != nil {
		return nil, call, resp, fmt.Errorf("%w: its arguments were not valid JSON: %w", ErrNoPlan, err)
	}
	steps, err := parseSteps(submitted.Steps)
	if err != nil {
		return nil, call, resp, fmt.Errorf("%w: its steps are not a list of steps: %w", ErrNoPlan, err)
	}
	if len(steps) == 0 {
		return nil, call, resp, errNothingToPlan
	}
	if n := len(steps); n > in.Spec.MaxSteps {
		return nil, call, resp, fmt.Errorf("%w: the plan asks for %d steps against a ceiling of %d",
			agent.ErrMaxSteps, n, in.Spec.MaxSteps)
	}

	return steps, call, resp, nil
}

// parseSteps reads a plan's steps: a list, or that list sent as a JSON
// string, as some models send an array argument. Absent or null is none.
func parseSteps(raw json.RawMessage) ([]step, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, err
		}
		raw = json.RawMessage(inner)
	}
	var steps []step
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// stepFiles are the files a plan's steps attached, by step.
type stepFiles struct {
	byCall map[string][]agent.Content
	order  []agent.ToolCall
}

func (l rewoo) execute(ctx context.Context, in agent.Input, plan []step) (string, stepFiles, error) {
	files := stepFiles{byCall: map[string][]agent.Content{}}
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
		out, failure, attach, suspended := invoke(ctx, byName, call)
		if len(attach) > 0 {
			files.byCall[id] = attach
			files.order = append(files.order, agent.ToolCall{ID: id, Name: s.Tool})
		}

		if ctx.Err() != nil {
			return "", files, ctx.Err()
		}
		if suspended != nil {
			return "", files, fmt.Errorf("%w: step %s, %s: %s", ErrCannotWait, id, s.Tool, suspended.Error())
		}

		in.Emit(ctx, agent.ToolResultEvent{
			ID: id, Name: s.Tool, Output: out,
			Err: failure, Duration: time.Since(started),
		})

		evidence = append(evidence, out)
		record(&b, id, s.Tool, args, out)
	}

	return b.String(), files, nil
}

func record(b *strings.Builder, id, name string, args json.RawMessage, out string) {
	fmt.Fprintf(b, "%s = %s(%s)\n  %s\n", id, name, args, out)
}

func (l rewoo) solve(ctx context.Context, in agent.Input, msgs []agent.Message, prompt agent.Content) (carried, error) {
	ask := in
	ask.Spec.System = append(slices.Clone(in.Spec.System), prompt)
	ask.Spec.Tools = nil

	resp, err := (rewoo{stream: l.stream}).call(ctx, ask, msgs)
	if err != nil {
		return carried{}, fmt.Errorf("solving: %w", err)
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})
	c, err := carryOn(ctx, ask, msgs, resp, rewoo{stream: l.stream}.call)
	if err != nil {
		return c, fmt.Errorf("solving: %w", err)
	}
	if endsInToolCall(c.text) {
		// Nothing carried: markup is no answer, so it is neither the output
		// nor a turn the runner would save into the session.
		return carried{}, fmt.Errorf("solving: %w", ErrToolMarkup)
	}
	return c, nil
}

// The tags of a tool call written as text, as Anthropic's models write one
// (invoke, inside function_calls) and as Qwen's and Hermes-style models do
// (tool_call). A close tag need not match its open one.
var (
	callOpen  = regexp.MustCompile(`<(?:antml:)?(?:invoke|tool_call)\b`)
	callClose = regexp.MustCompile(`</(?:antml:)?(?:invoke|tool_call)>`)
	callWrap  = regexp.MustCompile(`(?:\s*</(?:antml:)?function_calls>)+\z`)
	paramOpen = regexp.MustCompile(`\A\s*<(?:antml:)?parameter\b`)
)

// endsInToolCall is whether an answer ends with a tool call written as text:
// the answer step has no tools, so a call there is the model trying to make
// one, which no person can use, whatever came before it. The last call is
// whole, ending the text; cut off after its parameters began; or cut off in
// its open tag. Prose that quotes a call and goes on is an answer.
func endsInToolCall(text string) bool {
	text = callWrap.ReplaceAllString(strings.TrimSpace(text), "")
	opens := callOpen.FindAllStringIndex(text, -1)
	if len(opens) == 0 {
		return false
	}
	last := text[opens[len(opens)-1][0]:]
	if closes := callClose.FindAllStringIndex(last, -1); len(closes) > 0 {
		return closes[len(closes)-1][1] == len(last)
	}
	end := strings.IndexByte(last, '>')
	if end < 0 {
		return true
	}
	rest := last[end+1:]
	return strings.TrimSpace(rest) == "" || paramOpen.MatchString(rest)
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

// unplanned is the answer step's prompt when no plan ran: nothing was
// looked up, and the model must not answer as if it had been.
var unplanned = agent.Content{
	Type: agent.ContentText,
	Text: "No tool ran, and none can run now. Answer from what you know; if the question needed something looked up, say plainly what you could not check. Do not write tool calls.",
}

var solving = agent.Content{
	Type: agent.ContentText,
	Text: "The plan has run. Answer the question from the evidence above. Where a step failed, say what could not be determined rather than guessing.",
}
