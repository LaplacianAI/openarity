// Command tools runs one agent turn that calls a tool.
//
// This is the smaller of the two examples and the one to read first: a spec the
// brain resolved, a streaming loop, and a tool whose closure would reach an MCP
// server under a team's credential.
//
//	go run ./examples/tools
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/loops"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

func main() {
	// os.Exit skips deferred calls, so the signal handler is released in
	// attempt rather than in a defer here that would never run.
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	// Ctrl-C matters here because a streaming run holds an open connection.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("count_issues", `{"repo":"openarity"}`),
		gateway.Answer("openarity has 3 open issues."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	// Wired once. A real deployment does this at startup and keeps the Runner
	// for the life of the process.
	runner, err := agent.New(openaicompat.Factory(), loops.ReActStreaming())
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Loop:     agent.LoopReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{countIssues()},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "How many open issues does openarity have?"}},
	}}

	// Events are consumed on their own goroutine. The loop never blocks on a
	// slow reader — Emit gives up when the context is done — but a reader that
	// never starts would still see nothing.
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
		return err
	}
	gateway.Summary(result)
	return nil
}

func countIssues() agent.Tool {
	return agent.Tool{
		Name:        "count_issues",
		Description: "Count the open issues in a repository. Use when asked how many issues exist.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"repo": {"type": "string", "description": "the repository name"}},
			"required": ["repo"]
		}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Repo string `json:"repo"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				// Returned, not failed: the model reads this and can correct
				// itself. Only a cancelled context ends a run.
				return "", fmt.Errorf("could not read the arguments: %w", err)
			}
			return fmt.Sprintf("%s has 3 open issues", in.Repo), nil
		},
	}
}
