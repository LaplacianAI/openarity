// Command discovery runs the same task twice against forty tools: once with
// every schema sent on every request, and once with the tools found by
// search, described and run through call_tool.
//
// The tokens each request reads are printed for both, which is the point:
// with Spec.Discovery the forty schemas are never sent, only the one the model
// asked to see.
//
//	go run ./examples/discovery
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

func main() {
	if err := attempt(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func attempt() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	fmt.Println("── every schema sent ──")
	sent, err := run(ctx, runner, false,
		gateway.ToolCall("tracker_close_issue", `{"repo":"openarity","number":42}`),
		gateway.Answer("Closed #42."),
	)
	if err != nil {
		return err
	}

	fmt.Println("\n\n── found by search ──")
	found, err := run(ctx, runner, true,
		gateway.ToolCall("find_tools", `{"query":"close an issue"}`),
		gateway.ToolCall(agent.DescribeToolsName, `{"names":["tracker_close_issue"]}`),
		gateway.ToolCall(agent.CallToolName, `{"name":"tracker_close_issue","arguments":{"repo":"openarity","number":42}}`),
		gateway.Answer("Closed #42."),
	)
	if err != nil {
		return err
	}

	fmt.Printf("\n\ninput tokens, whole run   every schema %d   found by search %d\n", sent, found)
	return nil
}

func run(ctx context.Context, runner *agent.Runner, search bool, script ...gateway.Turn) (int, error) {
	endpoint, shutdown := gateway.Resolve(script...)
	defer shutdown()

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a terse assistant. Use the tools you are given."),
		Tools:    []agent.Tool{findTools()},
		MaxSteps: 6,
	}
	if search {
		spec.Discovery = &agent.Discovery{
			Search: "find_tools",
			Tools:  tracker(),
			Groups: []agent.Group{{Name: "tracker", About: "the issue tracker", Tools: names(tracker())}},
		}
	} else {
		spec.Tools = append(spec.Tools, tracker()...)
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "Close issue 42 in openarity."}},
	}}

	events := make(chan agent.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = gateway.Report(events)
	}()

	result, err := runner.Run(ctx, spec, msgs, endpoint, events)
	close(events)
	<-done
	return result.Usage.InputTokens, err
}

// findTools stands in for a real search, such as the brain's graph: it
// matches the query's words against the tools' descriptions.
func findTools() agent.Tool {
	return agent.Tool{
		Name:        "find_tools",
		Description: "Find tools by what they do. Returns names and descriptions.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			var hits []string
			for _, t := range tracker() {
				for _, w := range strings.Fields(in.Query) {
					if len(w) > 3 && strings.Contains(t.Description, w) {
						hits = append(hits, t.Name+": "+t.Description)
						break
					}
				}
			}
			return strings.Join(hits, "\n"), nil
		},
	}
}

// tracker is forty tools, each with a schema the size a real MCP server's is.
func tracker() []agent.Tool {
	verbs := []string{"close", "open", "label", "assign", "comment on", "lock", "pin", "move", "link", "watch"}
	things := []string{"issue", "milestone", "project card", "discussion"}

	var out []agent.Tool
	for _, thing := range things {
		for _, verb := range verbs {
			name := "tracker_" + strings.ReplaceAll(verb, " ", "_") + "_" + strings.ReplaceAll(thing, " ", "_")
			out = append(out, agent.Tool{
				Name:        name,
				Description: fmt.Sprintf("%s an %s in a repository's tracker", verb, thing),
				Schema: json.RawMessage(fmt.Sprintf(`{
					"type": "object",
					"properties": {
						"repo":   {"type": "string", "description": "the repository the %[1]s is in, as owner/name or name"},
						"number": {"type": "integer", "description": "the %[1]s's number, as shown in its URL"},
						"reason": {"type": "string", "description": "why, written to the %[1]s's history for whoever reads it later"},
						"notify": {"type": "boolean", "description": "whether everyone watching the %[1]s is told"}
					},
					"required": ["repo", "number"]
				}`, thing)),
				Invoke: func(_ context.Context, args json.RawMessage) (string, error) {
					return fmt.Sprintf("%s done: %s", name, args), nil
				},
			})
		}
	}
	return out
}

func names(tools []agent.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}
