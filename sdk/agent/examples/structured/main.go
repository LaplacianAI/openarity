package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

var schema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"file": {"type": "string"},
		"line": {"type": "integer"},
		"severity": {"type": "string", "enum": ["low", "high"]}
	},
	"required": ["file", "line", "severity"],
	"additionalProperties": false
}`)

type finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
}

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("── The model answers in the schema ──────────────────────")
	if err := inline(ctx); err != nil {
		return err
	}

	fmt.Println("\n── A parser model turns prose into the schema ───────────")
	return parsed(ctx)
}

func inline(ctx context.Context) error {
	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("search", `{"query":"failing test"}`),
		gateway.Answer(`{"file":"vault.go","line":88,"severity":"high"}`),
	)
	defer shutdown()

	seen := &recorder{}
	runner, err := agent.New(seen.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You find the cause of a failing test. Always call the search tool before answering — never answer from memory."),
		Tools:    []agent.Tool{search()},
		MaxSteps: 5,
		OutputSchema: &agent.OutputSchema{
			Name:        "finding",
			Description: "Where the failure is and how bad it is.",
			JSON:        schema,
			Strict:      true,
		},
	}

	result, err := run(ctx, runner, spec, endpoint)
	if err != nil {
		return err
	}

	report(seen, result)
	return nil
}

func parsed(ctx context.Context) error {
	endpoint, shutdown := gateway.Resolve(
		gateway.ToolCall("search", `{"query":"failing test"}`),
		gateway.Answer("It fails at vault.go line 88: renewLease returns a nil lease. Serious."),
		gateway.Answer(`{"file":"vault.go","line":88,"severity":"high"}`),
	)
	defer shutdown()

	seen := &recorder{}
	runner, err := agent.New(seen.wrap(openaicompat.Factory()), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	parser := os.Getenv("OPENARITY_PARSER_MODEL")
	if parser == "" {
		fmt.Println("no OPENARITY_PARSER_MODEL set — the parser falls back to the run's own model")
	}

	spec := agent.Spec{
		Model:       agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:     agent.PatternReAct,
		System:      agent.System("You find the cause of a failing test. Always call the search tool before answering, then answer in a sentence or two."),
		Tools:       []agent.Tool{search()},
		MaxSteps:    5,
		Parser:      true,
		ParserModel: agent.ModelRef{Name: parser, MaxTokens: 1024},
		OutputSchema: &agent.OutputSchema{
			Name:        "finding",
			Description: "Where the failure is and how bad it is.",
			JSON:        schema,
			Strict:      true,
		},
	}

	result, err := run(ctx, runner, spec, endpoint)
	if err != nil {
		return err
	}

	report(seen, result)
	return nil
}

func run(ctx context.Context, runner *agent.Runner, spec agent.Spec,
	endpoint agent.Endpoint,
) (agent.Result, error) {
	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Why is the test failing?"}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() { defer close(done); gateway.Report(events) }()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done

	return result, err
}

func report(seen *recorder, result agent.Result) {
	fmt.Println()

	carried, bare, names := seen.carried()
	fmt.Printf("requests    %d carried the schema, %d carried none\n", carried, bare)
	if len(names) > 0 {
		fmt.Printf("named       %s\n", strings.Join(names, ", "))
	}

	if result.Structured == nil {
		fmt.Printf("structured  nothing — the model answered in prose:\n            %s\n", result.Output)
		return
	}

	var got finding
	if err := json.Unmarshal(result.Structured, &got); err != nil {
		fmt.Printf("structured  %s — which does not unmarshal: %v\n", result.Structured, err)
		return
	}

	fmt.Printf("structured  %s\n", result.Structured)
	fmt.Printf("typed       file=%s line=%d severity=%s\n", got.File, got.Line, got.Severity)
	if result.Plain != result.Output {
		fmt.Printf("plain       what the model actually said, kept either way:\n            %s\n",
			result.Plain)
	}
	fmt.Printf("severity    %q is one of the two the schema allows, not a word the model picked\n",
		got.Severity)

	gateway.Summary(result)
}

func search() agent.Tool {
	return agent.Tool{
		Name:        "search",
		Description: "Search the codebase.",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string"}},
			"required": ["query"]
		}`),
		Invoke: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "vault.go:88  renewLease returns a nil lease when the token is already expired", nil
		},
	}
}

type recorder struct {
	mu    sync.Mutex
	sent  []*agent.OutputSchema
	names map[string]bool
}

func (r *recorder) wrap(inner agent.ClientFactory) agent.ClientFactory {
	return func(e agent.Endpoint) (agent.ModelClient, error) {
		client, err := inner(e)
		if err != nil {
			return nil, err
		}
		return &recordingClient{inner: client, to: r}, nil
	}
}

func (r *recorder) add(out *agent.OutputSchema) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sent = append(r.sent, out)
	if out != nil {
		if r.names == nil {
			r.names = map[string]bool{}
		}
		r.names[out.Name] = true
	}
}

func (r *recorder) carried() (with, without int, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, out := range r.sent {
		if out == nil {
			without++
			continue
		}
		with++
	}
	for name := range r.names {
		names = append(names, name)
	}
	return with, without, names
}

type recordingClient struct {
	inner agent.ModelClient
	to    *recorder
}

func (c *recordingClient) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	c.to.add(req.OutputSchema)
	return c.inner.Complete(ctx, req)
}

func (c *recordingClient) Stream(ctx context.Context, req agent.Request) (agent.Stream, error) {
	c.to.add(req.OutputSchema)
	return c.inner.Stream(ctx, req)
}
