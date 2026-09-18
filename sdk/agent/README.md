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
no database, no server and no configuration file to set up.

Two direct dependencies, and which of them you build depends on what you
import: `github.com/openai/openai-go/v3` behind the model client, and the
official MCP SDK behind `tools/mcp`. The core and the patterns link neither —
a `make check` step fails if they ever do.

`go get` resolves to `v0.1.0`. It is a `v0`, so nothing about the API is
promised yet — pin the version rather than tracking `@latest`, and read what
changed before moving.

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
`ToolCallEvent`, `ToolResultEvent`, `UsageEvent`, `StepEvent` and `SteerEvent`
arrive as the run happens.

## Steering a run

`Run` waits. `Start` hands you a handle while it works, so you can say
something to a run already in progress:

```go
run := runner.Start(ctx, spec, msgs, endpoint, events)

go func() {
    for ev := range events {
        if call, ok := ev.(agent.ToolCallEvent); ok && wrongDirection(call) {
            run.Steer("the bug is in vault.go — stop reading tests")
        }
    }
}()

result, err := run.Wait()
```

Nothing is interrupted. The model is not listening while a pattern works — it
is called, it answers, and between those two moments it has no ears. So a steer
waits, and rides the next request out.

It lands as a user message of its own, at the index in the conversation where
it arrived — not appended to a tool result, and not moved to the end of every
later request. The provider only forbids a user message *between* a tool call
and its result; after the results it is legal. A steer kept permanently last
reads to the model as a fresh instruction every turn, and the run never
concludes.

**No pattern implements this.** Patterns reach the model through
`ModelClient`, so the runner wraps that — the same seam it already uses to
total usage. ReAct, Plan, ReWOO, Reflection and any pattern you write yourself
are steerable without a line of change, and a pattern cannot forget to support
it.

Two things follow from the model having no ears, and both are visible rather
than silent:

- `Steer` returns `ErrRunFinished` once there is no further request to carry it.
- A steer sent during what turns out to be the last call comes back on
  `Result.UnappliedSteers` rather than disappearing. A steer that silently
  vanishes is indistinguishable from one the model read and ignored.

Set `Spec.SteerContinuations` above zero and a steer left pending at the end
does not have to stop there: it becomes an ordinary user message and the pattern
runs again on the extended conversation, up to that many extra turns. Zero, the
default, is the behaviour above. The count is a bound rather than a switch
because each continuation gets a fresh `MaxSteps`, so nothing else limits a run
that is steered over and over. One `Result` comes back either way, with `Steps`
and `Usage` summed across every turn.

`Run` is unchanged and still the right call when you only want the answer — it
is `Start(...).Wait()`.

Three examples run it end to end: [`steering`](examples/steering) for the
mechanism, [`steering-typed`](examples/steering-typed) for a person typing at a
working agent, and [`steering-limits`](examples/steering-limits) for the two
places it stops doing what you might hope.

## Structured output

`Spec.OutputSchema` puts a JSON Schema on every request the run makes, as
`response_format` — a feature of the OpenAI-compatible API, so it reaches
whatever your gateway reaches. No pattern implements it: the runner wraps
`ModelClient`, the same seam it already uses to total usage.

```go
spec.OutputSchema = &agent.OutputSchema{Name: "finding", JSON: schema}
...
var found finding
if result.Structured != nil {
	json.Unmarshal(result.Structured, &found)
}
```

`Result` carries three forms of the answer. `Plain` is what the model said and
is always set; `Structured` is that parsed, or nil; `Output` is the structured
one when there is one and `Plain` when there is not — so a caller that does not
care which mode produced the answer reads `Output`.

`Parser: true` moves the schema off the loop: the run answers in prose, and one
further call — to `ParserModel`, or the run's own model if that is empty — turns
the transcript into the schema. One ordinary model call, counted in
`Result.Usage` like any other.

**`Structured` can be nil even when the gateway accepted the schema**, because
`response_format` is not enforced everywhere; roughly one run in three came back
as prose against OmniRoute. That is not an error — an unmet schema leaves
`Structured` nil and `Plain` intact, and the caller decides whether it can
proceed. A model that answered correctly but
wrapped it in a markdown code fence is unwrapped, including when a sentence
comes first.

[`examples/structured`](examples/structured) runs both modes and prints how many
requests carried the schema, which is the whole difference between them.

## Patterns

| Constructor             | Streaming variant       | What it does                                    |
| ----------------------- | ----------------------- | ----------------------------------------------- |
| `patterns.ReAct()`      | `ReActStreaming()`      | think, call a tool, look, repeat                |
| `patterns.Plan()`       | `PlanStreaming()`       | one planning call with no tools, then ReAct     |
| `patterns.ReWOO()`      | `ReWOOStreaming()`      | plan every call up front, run them, then answer |
| `patterns.Reflection(n)`| `ReflectionStreaming(n)`| answer, critique it, rewrite — up to n times    |

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
