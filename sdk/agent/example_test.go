// This file is package agent_test rather than agent, and that is not a style
// choice: openaicompat imports agent, so an in-package test importing it back
// would be an import cycle. The compiler refusing that is what proves the
// dependency only ever points one way.
package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/loops"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

// Example wires everything the way the brain will: a Runner built once with the
// loops it knows and a client factory, then a run carrying a fully resolved
// Spec and nothing but a URL and a key for the endpoint.
//
// Each package is tested on its own elsewhere. This is the one that fails if
// they stop fitting together, which no amount of per-package coverage catches.
func Example() {
	// Stand in for a LiteLLM or OmniRoute gateway. The first call asks for a
	// tool, the second answers with what the tool returned.
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,
				"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function",
				"function":{"name":"count_issues","arguments":"{\"repo\":\"openarity\"}"}}]},
				"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":90,"completion_tokens":12}}`)
			return
		}
		fmt.Fprint(w, `{"id":"2","object":"chat.completion","choices":[{"index":0,
			"message":{"role":"assistant","content":"There are 3 open issues."},
			"finish_reason":"stop"}],
			"usage":{"prompt_tokens":140,"completion_tokens":8}}`)
	}))
	defer gateway.Close()

	// Wired once, at startup.
	runner, err := agent.New(openaicompat.Factory(), loops.ReAct())
	if err != nil {
		fmt.Println("wiring:", err)
		return
	}

	// Resolved by the brain: which model, which loop, which tools, and a
	// ceiling. The tool's Invoke is a closure the brain built over an MCP
	// server and a team's credential; here it is a stub.
	spec := agent.Spec{
		Model: agent.ModelRef{Name: "anthropic/claude-opus-5", MaxTokens: 1024},
		Loop:  agent.LoopReAct,
		System: []agent.Content{
			{Type: agent.ContentText, Text: "You are a helpful agent.", Cacheable: true},
		},
		Tools: []agent.Tool{{
			Name:        "count_issues",
			Description: "Count open issues in a repository",
			Schema:      json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`),
			Invoke: func(context.Context, json.RawMessage) (string, error) {
				return "3", nil
			},
		}},
		MaxSteps: 5,
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "how many open issues?"}},
	}}

	// Per run: a URL and a key, nothing more. The SDK connects.
	result, err := runner.Run(context.Background(), spec, msgs,
		agent.Endpoint{BaseURL: gateway.URL, APIKey: "sk-team-a"}, nil)
	if err != nil {
		fmt.Println("run:", err)
		return
	}

	fmt.Println(result.Output)
	fmt.Println("steps:", result.Steps)
	fmt.Println("messages:", len(result.Messages))
	fmt.Println("input tokens:", result.Usage.InputTokens)

	// Output:
	// There are 3 open issues.
	// steps: 2
	// messages: 4
	// input tokens: 230
}
