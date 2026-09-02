# `sdk/agent`

The agent loop, as a Go library. It receives a fully resolved spec — a model, a
pattern, a system prompt, tools and skills — runs the loop, and returns the
conversation it produced.

It decides nothing. It does not choose a model, authorise a tool, or persist a
message — every one of those is the caller's, handed in per run. So it drops
into any Go program: a web service, a CLI, a worker, a test.

## Install

```sh
go get github.com/LaplacianAI/openarity/sdk/agent
```

Needs Go 1.26.6. Nothing else from this repository comes with it, and there is
no database, no server and no configuration file to set up. The only direct
dependency is `github.com/openai/openai-go/v3`.

There is no tagged release yet, so `go get` resolves to a pseudo-version of
`main`. Pin the commit you tested against until one exists.

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

## The pieces

**`Runner`** is built once and shared. `New` takes a `ClientFactory` and the
patterns your program offers; a pattern name the runner does not hold is refused
by name rather than silently defaulted.

**`Endpoint`** is per call, not per runner — a base URL and a key. One runner
serves callers on different gateways, or on one gateway with a credential each,
and the factory caches a client per endpoint so this costs nothing.

**`Result.Messages`** is the conversation the run produced: what you passed in,
extended. Hand it back on the next turn and the agent has memory. The library
stores nothing, so where that slice lives is yours to decide.

**`Result.Usage`** is counted at the model client, not by the pattern. A pattern
written outside this module gets the right number without knowing to try, and a
run that failed half way still reports what it spent.

**`events chan<- Event`** is optional; pass `nil` to ignore it. `TextEvent`,
`ToolCallEvent`, `ToolResultEvent`, `UsageEvent` and `StepEvent` arrive as the
run happens.

## Patterns

| Constructor        | Streaming variant  | What it does                                     |
| ------------------ | ------------------ | ------------------------------------------------ |
| `patterns.ReAct()` | `ReActStreaming()` | think, call a tool, look, repeat                 |
| `patterns.Plan()`  | `PlanStreaming()`  | one planning call with no tools, then ReAct      |
| `patterns.ReWOO()` | `ReWOOStreaming()` | plan every call up front, run them, then answer  |

ReWOO costs two model calls whatever the plan holds, and no tool's output can
change what the agent set out to do. That only *saves* tokens on a dependent
chain: a current model asks for independent calls together in one turn, so ReAct
pays one call for all of them.

`Pattern` is an interface, so you can register your own — or wrap a shipped one
in your own policy — without changing the library. See
[`examples/custom`](examples/custom) for one enforcing a token ceiling.

## Tools and skills

A `Tool` carries its own `Invoke` closure, so an MCP server, a credential and a
network call all stay on the caller's side of the boundary. The library only
ever calls the function.

A `Skill` spends its `Description` in the system prompt and its `Body` only when
the model asks for it. Every skill arrives through one `Skill` tool, so offering
sixty of them costs one entry in the tool list — which matters because the tool
array is the front of the cached prefix, and a tool per skill would give every
caller a different one.

## Models

`models/openaicompat` speaks OpenAI chat completions, so it reaches LiteLLM,
OmniRoute, or a provider directly. Anything satisfying `agent.ModelClient` works
as well; `ClientFactory` is the seam.

## Examples

```sh
go run ./examples/tools
```

Each runs with nothing installed — with no `OPENARITY_MODELS_BASE_URL` set they
start a stub gateway in-process that speaks the streaming protocol, so the
pattern, the accumulator and the tool dispatch are all real. See
[`examples/README.md`](examples/README.md).

## Development

```sh
make check
```

Runs tidy, format, vet, lint, the module boundary check, the build, coverage and
`govulncheck`. Coverage sits at 100% and `make check` fails below 95%. See the
repository's [CONTRIBUTING.md](../../CONTRIBUTING.md).
