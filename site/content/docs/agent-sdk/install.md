---
title: Install and first agent
weight: 1
---

```sh
go get github.com/LaplacianAI/openarity/sdk/agent
```

Needs Go 1.26.6. There are two direct dependencies and you build only the ones
you import: `github.com/openai/openai-go/v3` sits behind
[`models/openaicompat`](#talking-to-a-real-gateway), and the official MCP SDK
behind [`tools/mcp`](../mcp). The core packages and the patterns link neither,
which a `make check` step enforces rather than assumes.

{{< callout type="warning" >}}
`go get` resolves to `v0.1.0`. It is a `v0`, so nothing about the API is
promised yet — pin the version rather than tracking `@latest`, and read what
changed before moving.
{{< /callout >}}

## A whole agent

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

func main() {
	runner, err := agent.New(openaicompat.Factory(), patterns.ReAct())
	if err != nil {
		panic(err)
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: "gpt-4o-mini"},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a helpful assistant. Answer in a sentence or two."),
		MaxSteps: 4,
		Tools: []agent.Tool{{
			Name:        "clock",
			Description: "The current time, as an RFC 3339 timestamp.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Invoke: func(context.Context, json.RawMessage) (string, error) {
				return time.Now().Format(time.RFC3339), nil
			},
		}},
	}

	msgs := []agent.Message{{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Type: agent.ContentText, Text: "What time is it?"}},
	}}

	endpoint := agent.Endpoint{
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
	}

	result, err := runner.Run(context.Background(), spec, msgs, endpoint, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Output)
}
```

That program is compiled verbatim from this page's source in CI, so it is the
code that works rather than the code that reads well.

## What each piece owns

**`Runner`** is built once and shared. `New` takes a `ClientFactory` and the
patterns your program offers. A pattern name the runner does not hold is refused
by name, listing the ones it has, rather than silently falling back to a
default.

**`Endpoint`** is per call, not per runner — a base URL and a key. One runner
serves callers on different gateways, or on one gateway with a credential each,
and the factory caches a client per endpoint so this costs nothing after the
first call.

**`Result.Messages`** is the conversation the run produced: what you passed in,
extended. Hand it back on the next turn and the agent has memory. The library
stores nothing, so where that slice lives is yours.

**`Result.Usage`** is counted at the model client rather than by the pattern.
That means a pattern written outside the module gets the right number without
knowing to try, and a run that failed half way still reports what it spent.

**`events`** is the last argument, a `chan<- agent.Event`. Pass `nil` to ignore
it. `TextEvent`, `ToolCallEvent`, `ToolResultEvent`, `UsageEvent` and
`StepEvent` arrive as the run happens.

## Talking to a real gateway

`models/openaicompat` speaks OpenAI chat completions, so the same client reaches
LiteLLM, OmniRoute, or a provider directly:

```sh
OPENAI_BASE_URL=http://127.0.0.1:20128/v1 \
OPENAI_API_KEY=sk-… \
go run .
```

Anything satisfying `agent.ModelClient` works as well — `ClientFactory` is the
seam, and it is one function.

## Examples

The repository has runnable programs under
[`sdk/agent/examples/`](https://github.com/LaplacianAI/openarity/tree/main/sdk/agent/examples).
Each starts a stub gateway in-process when no real one is configured, so they
run with no credentials while still exercising the real pattern, the real
streaming accumulator and the real tool dispatch.

```sh
go run ./examples/tools
```
