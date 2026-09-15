package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
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

	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("search", `{"query":"failing test"}`),
		gateway.Answer("The failure is in vault.go, not in the tests."),
	)
	defer shutdown()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	typed := make(chan struct{})

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{search(typed)},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	reported := make(chan struct{})

	sawTool := make(chan struct{})

	run := runner.Start(ctx, spec, msgs, endpoint, events)

	go func() {
		defer close(reported)
		for ev := range events {
			switch e := ev.(type) {
			case agent.ToolCallEvent:
				fmt.Printf("tool     %s %s\n", e.Name, e.Arguments)
				close(sawTool)
			case agent.SteerEvent:
				fmt.Printf("steer    reached a request: %q\n", e.Text)
			}
		}
	}()

	go func() {
		defer close(typed)
		for _, line := range lines(ctx, sawTool) {
			if err := run.Steer(line); err != nil {

				fmt.Printf("steer    not applied — %v\n", err)
				return
			}
			fmt.Printf("steer    queued\n")
		}
	}()

	result, err := run.Wait()
	close(events)
	<-reported
	if err != nil {
		return err
	}

	if len(result.UnappliedSteers) > 0 {
		fmt.Printf("\nunapplied  %d — the run ended before a request could carry them\n",
			len(result.UnappliedSteers))
	}
	gateway.Summary(result)
	return nil
}

func lines(ctx context.Context, sawTool <-chan struct{}) []string {
	if os.Getenv("STEER_FROM_STDIN") == "" {
		fmt.Println("steering with a scripted line — set STEER_FROM_STDIN=1 to type your own")
		fmt.Println()

		select {
		case <-sawTool:
		case <-ctx.Done():
			return nil
		}
		return []string{"the failure is in vault.go, stop reading tests"}
	}

	fmt.Println("Type to steer it, Enter to send. Ctrl-D when you are done.")
	fmt.Print("> ")

	var out []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return out
		}
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
		fmt.Print("> ")
	}
	return out
}

func search(typed <-chan struct{}) agent.Tool {
	return agent.Tool{
		Name:        "search",
		Description: "Search the codebase.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string"}},
			"required": ["query"]
		}`),
		Invoke: func(ctx context.Context, _ json.RawMessage) (string, error) {
			select {
			case <-typed:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "12 files match", nil
		},
	}
}
