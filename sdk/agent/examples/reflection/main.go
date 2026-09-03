// Command reflection runs the same task twice, once with ReAct and once with
// Reflection, and prints what each answered and what it cost.
//
// The task has a wrong first draft on purpose: the stub's opening answer says
// Germany's capital is Paris. ReAct has no way to notice — a pattern that stops
// when the model stops calling tools has already returned by then. Reflection
// asks a second time whether the answer holds, and the answer to that question
// is what it acts on.
//
// So the two columns below are not about tokens. They are about whether a wrong
// answer survives, and the price of finding out is one extra call when the
// critique approves and two when it does not.
//
//	go run ./examples/reflection
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
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

const (
	question = "Which city is the capital of Germany, and since when?"

	// The draft the stub gives both patterns. Wrong in a way a reader can
	// check, rather than wrong in a way only the example knows about.
	draft = "Paris has been the capital of Germany since 1949."

	// What the critic finds. Named here so the run can print it: the pattern
	// reads the critique out of a tool call and never emits it as an event,
	// so there is nothing for gateway.Report to show.
	finding = "Paris is the capital of France. Germany's capital moved to Berlin " +
		"in 1990 on reunification; Bonn was the West German capital from 1949."

	rewrite = "Berlin has been the capital of Germany since reunification in 1990. " +
		"Bonn served as West Germany's capital from 1949 until then."
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

	fmt.Printf("model    %s\nasked    %s\n", gateway.Model(), question)

	// ReAct sees one turn and returns it. There is no second opinion to have.
	react, err := run(ctx, "react", patterns.ReActStreaming(),
		gateway.Answer(draft),
	)
	if err != nil {
		return err
	}

	// Reflection asks again. The critique is a tool call rather than prose,
	// so "needs_refinement" is a boolean the pattern reads rather than a
	// sentence it has to interpret.
	reflect, err := run(ctx, "reflection", patterns.ReflectionStreaming(1),
		gateway.Answer(draft),
		gateway.ToolCall(patterns.CritiqueToolName, critique(true, finding)),
		gateway.Answer(rewrite),
	)
	if err != nil {
		return err
	}

	fmt.Printf("\n%-12s %-6s %-8s %s\n", "", "turns", "tokens", "answer")
	for _, r := range []outcome{react, reflect} {
		fmt.Printf("%-12s %-6d %-8d %s\n", r.name, r.turns,
			r.usage.InputTokens+r.usage.OutputTokens, r.answer)
	}

	fmt.Printf("\nThe critique that produced the rewrite:\n  %s\n", finding)
	fmt.Println("\nA critique that approves costs one call and changes nothing.")
	return nil
}

type outcome struct {
	name   string
	turns  int
	usage  agent.Usage
	answer string
}

func run(ctx context.Context, name string, p agent.Pattern, script ...gateway.Turn) (outcome, error) {
	endpoint, shutdown := gateway.Resolve(script...)
	defer shutdown()

	runner, err := agent.New(openaicompat.Factory(), p)
	if err != nil {
		return outcome{}, err
	}

	spec := agent.Spec{
		Model:   agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern: p.Name(),
		System:  agent.System("You are a terse assistant. Answer in one or two sentences."),
		// Three, because one cycle costs one call to answer and two to check
		// and rewrite. Reflection refuses at the start rather than running out
		// half way through a rewrite.
		MaxSteps: 3,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: question}},
	}}

	fmt.Printf("\n── %s ──\n", name)

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	var turns int
	go func() {
		defer close(done)
		turns, _ = gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done
	if err != nil {
		return outcome{}, err
	}
	fmt.Println()

	return outcome{
		name:   name,
		turns:  turns,
		usage:  result.Usage,
		answer: strings.TrimSpace(result.Output),
	}, nil
}

// critique builds what the reflecting turn is scripted to submit. Marshalled
// rather than written by hand so a change to the tool's shape breaks here
// rather than producing arguments the pattern silently cannot read.
func critique(needs bool, note string) string {
	args, err := json.Marshal(map[string]any{
		"needs_refinement": needs,
		"critique":         note,
	})
	if err != nil {
		panic(err)
	}
	return string(args)
}
