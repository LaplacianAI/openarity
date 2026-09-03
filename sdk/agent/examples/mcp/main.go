// Command mcp gives an agent the tools of an MCP server.
//
// The loop never learns that MCP exists. tools/mcp connects, converts what the
// server offers into agent.Tool, and the spec carries them like any other —
// which is the whole point: a pattern dispatches an MCP call and a closure the
// same way.
//
// The MCP server here runs in this process over HTTP, so the example needs
// nothing installed. A real one changes only the Server literal:
//
//	mcp.Server{Name: "docs", URL: "https://mcp.example.com/mcp"}
//	mcp.Server{Name: "github", Command: []string{"npx", "-y", "@modelcontextprotocol/server-github"},
//		Env: []string{"GITHUB_TOKEN=" + os.Getenv("GITHUB_TOKEN")}}
//
//	go run ./examples/mcp
//
// See package gateway for pointing it at a real LiteLLM or OmniRoute.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/examples/gateway"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
	"github.com/LaplacianAI/openarity/sdk/agent/tools/mcp"
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

	url, shutdown, err := serveMCP(ctx)
	if err != nil {
		return err
	}
	defer shutdown()

	// The name is not decoration: it prefixes every tool this server offers,
	// so a second server's "count_issues" cannot quietly replace this one.
	session, err := mcp.Connect(ctx, mcp.Server{Name: "repo", URL: url})
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	tools, err := mcp.Gather(session)
	if err != nil {
		return err
	}

	fmt.Printf("mcp      %s\n", url)
	for _, t := range tools {
		fmt.Printf("  tool   %s — %s\n", t.Name, t.Description)
	}

	endpoint, closeGateway := gateway.Resolve(
		gateway.ToolCall("repo__count_issues", `{"repo":"openarity"}`),
		gateway.Answer("openarity has 3 open issues."),
	)
	defer closeGateway()

	fmt.Printf("gateway  %s\nmodel    %s\n\n", endpoint.BaseURL, gateway.Model())

	runner, err := agent.New(openaicompat.Factory(), patterns.ReActStreaming())
	if err != nil {
		return err
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: gateway.Model(), MaxTokens: 1024},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a helpful assistant. Use the tools you are given."),
		Tools:    tools,
		MaxSteps: 4,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "How many open issues does openarity have?"}},
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
	if err != nil {
		return err
	}

	gateway.Summary(result)
	return nil
}

// serveMCP starts an MCP server on a loopback port and returns its URL.
//
// In process rather than as a child, so the example runs with nothing
// installed and nothing left behind if it is interrupted.
func serveMCP(ctx context.Context) (string, func(), error) {
	server := sdk.NewServer(&sdk.Implementation{Name: "repo", Version: "v1"}, nil)

	server.AddTool(&sdk.Tool{
		Name:        "count_issues",
		Description: "Count the open issues in a repository. Use when asked how many issues exist.",
		InputSchema: json.RawMessage(
			`{"type":"object","properties":{"repo":{"type":"string","description":"the repository name"}},"required":["repo"]}`),
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var in struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			// Deliberately not a protocol error. MCP wants a tool's own failure
			// in the result with IsError set, because a protocol error is
			// invisible to the model and it cannot correct itself.
			return &sdk.CallToolResult{ //nolint:nilerr // IsError is how MCP reports this
				IsError: true,
				Content: []sdk.Content{&sdk.TextContent{
					Text: fmt.Sprintf("the arguments were not readable: %v", err),
				}},
			}, nil
		}
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: fmt.Sprintf("%s has 3 open issues", in.Repo)},
		}}, nil
	})

	server.AddTool(&sdk.Tool{
		Name:        "latest_release",
		Description: "The most recent release tag of a repository.",
		InputSchema: json.RawMessage(
			`{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`),
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "sdk/agent/v0.1.0"},
		}}, nil
	})

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listening for the MCP server: %w", err)
	}

	httpServer := &http.Server{
		Handler:           sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = httpServer.Serve(listener) }()

	shutdown := func() {
		down, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(down)
	}

	return "http://" + listener.Addr().String(), shutdown, nil
}
