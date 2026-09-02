// Command rewoo runs the same task twice, once with ReAct and once with ReWOO,
// and prints what each cost.
//
// The task is a chain: find the latest release, then count what has been filed
// since it. The second call needs the first one's answer, which is the case
// where the two patterns genuinely differ.
//
// Independent calls are not that case. A current model asks for those together
// in one turn, so ReAct pays one model call for all of them and ReWOO saves
// nothing — the comparison in the ReWOO paper assumes one call per turn, which
// measures a model generation that has passed.
//
// A chain cannot be batched. ReAct has to see each answer before it can ask the
// next question, and every turn re-sends everything before it. ReWOO writes the
// whole chain down first, with #E1 standing in for what it cannot know yet, and
// still pays two calls. The stub prices a request by its size, so the token
// columns measure this rather than asserting it.
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

	// One turn per link in the chain, because ReAct cannot ask the second
	// question until it has the first answer.
	react, err := compare(ctx, "react", patterns.ReActStreaming(),
		gateway.ToolCall("latest_release", `{"repo":"brain"}`),
		gateway.ToolCall("issues_since", `{"repo":"brain","tag":"v0.4.1"}`),
		gateway.Answer("7 issues have been filed since v0.4.1."),
	)
	if err != nil {
		return err
	}

	// ReWOO writes the chain down before anything runs. It cannot know the tag,
	// so it writes #E1 where the tag goes and the loop fills it in.
	rewoo, err := compare(ctx, "rewoo", patterns.ReWOOStreaming(),
		gateway.ToolCall(patterns.PlanToolName, `{"steps":[`+
			`{"tool":"latest_release","args":{"repo":"brain"},"why":"find the tag to count from"},`+
			`{"tool":"issues_since","args":{"repo":"brain","tag":"#E1"},"why":"count what came after it"}]}`),
		gateway.Answer("7 issues have been filed since v0.4.1."),
	)
	if err != nil {
		return err
	}

	fmt.Printf("\n%-8s %-6s %-6s %s\n", "", "turns", "tools", "tokens")
	for _, r := range []run{react, rewoo} {
		fmt.Printf("%-8s %-6d %-6d %d in, %d out\n",
			r.name, r.turns, r.tools, r.usage.InputTokens, r.usage.OutputTokens)
	}

	fmt.Printf("\nreact made %d model calls for %d tool calls; rewoo made %d for %d.\n",
		react.turns, react.tools, rewoo.turns, rewoo.tools)
	fmt.Printf("input tokens on the last turn: react %d, rewoo %d.\n",
		react.lastInput, rewoo.lastInput)
	return nil
}

type run struct {
	name  string
	turns int
	tools int
	usage agent.Usage

	// lastInput is what the final turn had to carry. It is the number the
	// comparison is actually about: ReAct's grows with every tool result,
	// ReWOO's does not.
	lastInput int
	answer    string
}

// compare runs one pattern and counts what it did. Model calls are counted from
// the events rather than taken from Result.Steps, because a step means
// something different in each pattern and the number this example is about is
// how often the model was asked. Counting them at the stub would report zero
// against a real gateway, which the stub never sees.
func compare(ctx context.Context, name string, p agent.Pattern, script ...gateway.Turn) (run, error) {
	endpoint, shutdown := gateway.Resolve(script...)
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
		Tools:    []agent.Tool{latestRelease(&tools), issuesSince(&tools)},
		MaxSteps: 8,
	}

	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{{
			Type: agent.ContentText,
			Text: "How many issues have been filed against brain since its latest release?",
		}},
	}}

	fmt.Printf("\n── %s ──\n", name)

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var turns, lastInput int
	go func() {
		defer close(done)
		turns, lastInput = gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done
	if err != nil {
		return run{}, err
	}
	fmt.Println()

	return run{
		name:      name,
		turns:     turns,
		lastInput: lastInput,
		tools:     int(tools.Load()),
		usage:     result.Usage,
		answer:    strings.TrimSpace(result.Output),
	}, nil
}

// latestRelease is the first link: nothing else can be asked until it answers.
func latestRelease(calls *atomic.Int32) agent.Tool {
	return agent.Tool{
		Name:        "latest_release",
		Description: "The tag of a repository's most recent release.",
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
			return "v0.4.1", nil
		},
	}
}

// issuesSince is the second link. Its tag argument is what ReWOO writes as #E1
// and the loop substitutes once the first step has run.
func issuesSince(calls *atomic.Int32) agent.Tool {
	return agent.Tool{
		Name:        "issues_since",
		Description: "How many issues were filed against a repository after a given release tag.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"repo": {"type": "string", "description": "the repository name"},
				"tag":  {"type": "string", "description": "the release tag to count from"}
			},
			"required": ["repo", "tag"]
		}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			calls.Add(1)

			var in struct {
				Repo string `json:"repo"`
				Tag  string `json:"tag"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("could not read the arguments: %w", err)
			}
			if in.Tag == "" {
				return "", fmt.Errorf("no tag was given, so there is nothing to count from")
			}
			return fmt.Sprintf("7 issues filed against %s since %s", in.Repo, in.Tag), nil
		},
	}
}
