// Command rewoo runs the same task twice, once with ReAct and once with ReWOO,
// and prints what each cost.
//
// The point is the model-call count. ReAct pays one turn per tool; ReWOO pays
// two whatever the plan holds, because the tools run with no model involved.
// With three repositories to count that is four turns against two, and the gap
// widens with every step a task needs.
//
//	go run ./examples/rewoo
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("model    %s\n", gateway.Model())

	// ReAct: one turn per tool, then one to answer. Four in total.
	react, err := compare(ctx, "react", patterns.ReActStreaming(),
		gateway.ToolCall("count_issues", `{"repo":"brain"}`),
		gateway.ToolCall("count_issues", `{"repo":"cli"}`),
		gateway.ToolCall("count_issues", `{"repo":"sdk"}`),
		gateway.Answer("brain 3, cli 0, sdk 1."),
	)
	if err != nil {
		return err
	}

	// ReWOO: the plan names all three calls at once, they run without the
	// model, and one turn answers from what they returned.
	rewoo, err := compare(ctx, "rewoo", patterns.ReWOOStreaming(),
		gateway.ToolCall(patterns.PlanToolName, `{"steps":[`+
			`{"tool":"count_issues","args":{"repo":"brain"},"why":"count the brain"},`+
			`{"tool":"count_issues","args":{"repo":"cli"},"why":"count the cli"},`+
			`{"tool":"count_issues","args":{"repo":"sdk"},"why":"count the sdk"}]}`),
		gateway.Answer("brain 3, cli 0, sdk 1."),
	)
	if err != nil {
		return err
	}

	fmt.Printf("\n%-8s %-6s %-6s %s\n", "", "turns", "tools", "tokens")
	for _, r := range []run{react, rewoo} {
		fmt.Printf("%-8s %-6d %-6d %d in, %d out\n",
			r.name, r.turns, r.tools, r.usage.InputTokens, r.usage.OutputTokens)
	}

	// Reported as measured rather than asserted in the prose above, because a
	// stub gateway bills a flat rate per turn and a real one does not.
	fmt.Printf("\nreact made %d model calls for %d tool calls; rewoo made %d for %d.\n",
		react.turns, react.tools, rewoo.turns, rewoo.tools)
	return nil
}

type run struct {
	name   string
	turns  int
	tools  int
	usage  agent.Usage
	answer string
}

// compare runs one pattern against a scripted gateway and counts what it did.
// Turns are counted at the gateway rather than taken from Result.Steps: a step
// means something different in each pattern, and the number this example is
// about is how often the model was asked.
func compare(ctx context.Context, name string, p agent.Pattern, script ...gateway.Turn) (run, error) {
	var turns atomic.Int32

	endpoint, shutdown := gateway.Resolve(gateway.Counted(&turns, script...)...)
	defer shutdown()

	runner, err := agent.New(openaicompat.Factory(), p)
	if err != nil {
		return run{}, err
	}

	var tools atomic.Int32
	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  p.Name(),
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{countIssues(&tools)},
		MaxSteps: 8,
	}

	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{{
			Type: agent.ContentText,
			Text: "How many open issues do brain, cli and sdk have?",
		}},
	}}

	fmt.Printf("\n── %s ──\n", name)

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
		return run{}, err
	}
	fmt.Println()

	return run{
		name:   name,
		turns:  int(turns.Load()),
		tools:  int(tools.Load()),
		usage:  result.Usage,
		answer: strings.TrimSpace(result.Output),
	}, nil
}

func countIssues(calls *atomic.Int32) agent.Tool {
	counts := map[string]string{"brain": "3", "cli": "0", "sdk": "1"}

	return agent.Tool{
		Name:        "count_issues",
		Description: "Count the open issues in a repository. Use when asked how many issues exist.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"repo": {"type": "string", "description": "the repository name"}},
			"required": ["repo"]
		}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			calls.Add(1)

			var in struct {
				Repo string `json:"repo"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("could not read the arguments: %w", err)
			}

			n, ok := counts[in.Repo]
			if !ok {
				return "", fmt.Errorf("no repository named %q", in.Repo)
			}
			return fmt.Sprintf("%s has %s open issues", in.Repo, n), nil
		},
	}
}
