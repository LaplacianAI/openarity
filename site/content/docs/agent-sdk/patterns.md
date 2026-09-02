---
title: Patterns
weight: 2
---

A pattern is the shape of the reasoning a run happens inside. `Runner.Run`
drives one; which one is a field on the spec, so the same runner serves all of
them.

| Constructor        | Streaming variant  | Model calls          |
| ------------------ | ------------------ | -------------------- |
| `patterns.ReAct()` | `ReActStreaming()` | one per step          |
| `patterns.Plan()`  | `PlanStreaming()`  | one, then one per step |
| `patterns.ReWOO()` | `ReWOOStreaming()` | two, always           |

## ReAct

Think, call a tool, look at what came back, repeat. `MaxSteps` is the ceiling on
model calls, and hitting it is an error rather than a truncated answer.

Every request carries the whole conversation so far, so step four sees the
assistant turns and tool results of steps one to three. Nothing summarises or
drops them — if a conversation outgrows the context window, that is yours to
handle today.

## Plan-then-act

One planning call with **no tools offered**, then ReAct with the plan appended
to the system prompt.

Fixing the plan before any tool output enters the transcript is what makes this
worth its extra call: it resists indirect prompt injection, because a tool
cannot change a plan that was already written.
[Control-flow integrity for agents](https://arxiv.org/abs/2509.08646) is the
argument in full.

It refuses `MaxSteps < 2` — one step cannot hold a plan and an action — and
refuses a planning call that tried to invoke a tool.

## ReWOO

Reasoning without observation. Every tool call is planned up front, they all
run, and one final call answers from the evidence. Two model calls whatever the
plan holds.

Steps refer to earlier ones by position:

```json
{
  "steps": [
    {
      "tool": "latest_release",
      "args": {"repo": "torvalds/linux"},
      "why": "find the tag"
    },
    {
      "tool": "issues_since",
      "args": {"tag": "#E1"},
      "why": "count what followed"
    }
  ]
}
```

`#E1` is substituted with step one's output before step two runs. A forward
reference is refused by name rather than sent as literal text.

{{< callout type="info" >}}
ReWOO only *saves* tokens on a dependent chain like the one above. Current
models ask for independent calls together in one turn, so ReAct pays a single
model call for all of them and ReWOO saves nothing — measured against a real
gateway, it cost more.
{{< /callout >}}

Measured on the dependent chain, same task, same gateway:

```text
         turns  tools  tokens
react    3      2      939 in, 36 out
rewoo    2      2      819 in, 24 out
```

## Writing your own

`Pattern` is an interface, so a pattern is anything with a name and a `Run`:

```go
type Pattern interface {
	Name() PatternName
	Run(ctx context.Context, in Input) (Result, error)
}
```

`Input.Model` is the only handle a pattern has on the outside world, and it is
already wrapped in the counter, so usage is correct whether or not the pattern
thinks about it.

The most useful shape is a wrapper rather than a new loop — take a shipped
pattern, enforce your own policy around it, register that under a name of your
own. [`examples/custom`](https://github.com/LaplacianAI/openarity/tree/main/sdk/agent/examples/custom)
does exactly that with a token ceiling.

Register it like any other:

```go
runner, err := agent.New(openaicompat.Factory(), patterns.ReAct(), myPattern{})
```
