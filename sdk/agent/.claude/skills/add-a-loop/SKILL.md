---
name: add-a-loop
description: Add a reasoning pattern to sdk/agent/loops. Use when implementing a new LoopType — code mode, plan-execute, reflexion — or when changing how an existing loop drives the model.
---

# Add a loop to `sdk/agent/loops`

A loop is the only thing in this module that decides *when* to call the model.
Everything else it needs — which model, which tools, which skills, how many
steps — arrives in the `Spec` already resolved. A loop that selects, ranks or
authorises anything is the bug this module's separate `go.mod` exists to
prevent.

## Step 1 — the file

One pattern per file, named after the pattern:

```text
loops/react.go   ReAct — think, call a tool, read the result, repeat
loops/code.go    code mode — the model writes code that calls the tools
```

Both constructors for a pattern live in the same file. `ReAct()` and
`ReActStreaming()` are one type with a `stream bool`, not two types, because
everything except the one call differs by nothing.

## Step 2 — the interface

```go
type Loop interface {
	Name() LoopType
	Run(ctx context.Context, in Input) (Result, error)
}
```

`Name` is what the brain puts in `Spec.Loop`. Add the constant to `spec.go`
alongside `LoopReAct` — a loop registering under a name no `LoopType` names is
unreachable, and nothing warns you.

Return an `agent.Loop`, not your concrete type. The struct stays unexported so
the constructor is the only way to build one.

## Step 3 — the four refusals, in this order

Every loop starts the same way and the order is load-bearing:

```go
if in.Model == nil {
	return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
}
if in.Spec.MaxSteps <= 0 {
	return agent.Result{}, agent.ErrNoMaxSteps
}
```

`MaxSteps` has no default and must not acquire one. A library that picks a
ceiling for somebody else's bill picks it wrong, and a looping model with no
ceiling runs until the context dies.

Then, inside the step loop, **before** dispatching anything:

```go
if resp.Finish == agent.FinishLength {
	return result, agent.ErrTruncated
}
if len(resp.Message.ToolCalls) == 0 {
	return result, nil
}
```

A turn cut at `MaxTokens` can carry a tool call with half-written JSON.
Inferring intent from `len(ToolCalls) > 0` dispatches it. Check `FinishLength`
first, always.

## Step 4 — the caller's slice is the caller's

```go
msgs := slices.Clone(in.Messages)
```

Not `msgs := in.Messages`. `append` into spare capacity writes into an array
the caller still holds, and a `Spec` reused for a second run comes back
carrying the first run's messages.

## Step 5 — a tool error is not a run error

```go
out, failure := invoke(ctx, byName, call)
```

Every failure becomes text the model reads and can recover from: an unknown
tool, arguments that are not valid JSON, a nil `Invoke`, an error the tool
returned. The run ends for exactly one reason — a cancelled context:

```go
if ctx.Err() != nil {
	return result, ctx.Err()
}
```

Checked **after** `Invoke`, not before. Before, and a tool that took the whole
remaining deadline reports success on a run that is already over.

## Step 6 — accumulate usage, and report the step

```go
result.Usage.InputTokens += resp.Usage.InputTokens
result.Usage.OutputTokens += resp.Usage.OutputTokens
result.Usage.CachedInputTokens += resp.Usage.CachedInputTokens
```

All three. `CachedInputTokens` is the number the prompt's whole ordering is
arranged around, and a loop that drops it makes a caching mistake invisible.

`result.Steps` is set every step, so a caller can tell "finished at step 3 of
10" from "cut off at 10 of 10". `ErrMaxSteps` is returned only when the loop
falls out of the range with the model still calling tools.

## Step 7 — events

`in.Emit(ctx, …)` is safe on a nil channel and gives up when the context is
done, so no call site needs a guard. Emit in the order things happen:

| Event             | When                                            |
| ----------------- | ----------------------------------------------- |
| `StepEvent`       | first thing in each step                        |
| `TextEvent`       | per delta, streaming only — a delta, not the sum |
| `UsageEvent`      | once the response's usage is known               |
| `ToolCallEvent`   | before dispatch                                 |
| `ToolResultEvent` | after, carrying `Duration` and `Err`            |

Never block on the channel yourself. `Emit` already handles a reader that
stopped reading.

## Step 8 — skills need nothing from you

By the time a loop sees the `Spec`, `withSkills` has already folded them into
`Spec.Tools` and `Spec.System`. A loop that reads `Spec.Skills` is doing work
the runner did, and would offer the skill tool twice.

## Step 9 — register it

`agent.New(factory, loops.ReAct(), loops.YourLoop())`. `New` refuses two loops
claiming one name and names both, so a clash is a startup error rather than a
map-iteration coin flip.

Update `examples/` if the new pattern is worth showing, and the README's line
about `loops/`.

## Step 10 — tests

`loops/<name>_test.go`. The module is at 100% and `make cover` enforces 95%.
At minimum:

1. **The happy path** — a model that answers without tools
2. **A tool round trip** — call, result, second turn, correct final output
3. **`ErrNoMaxSteps`** — and that it ran nothing
4. **`ErrMaxSteps`** — a model that calls tools forever
5. **`ErrTruncated`** — `FinishLength` *with* a tool call present, proving the
   call was not dispatched
6. **Every `invoke` failure** — unknown tool, bad JSON, nil `Invoke`, tool error
   — each producing a tool result rather than an error
7. **Cancellation mid-tool** — the run stops, the result carries what happened
8. **The caller's slice is unchanged** — build it with spare capacity
   (`make([]Message, 1, 4)`) or the bug cannot reproduce

Then break each guard and confirm a test fails. A guard-break that produces no
test output is usually a compile error, not a pass — read the whole output.

```sh
cd sdk/agent && make check
```

`make check` includes `boundary`, which fails if `loops` links a provider SDK.
Everything about a provider arrives through `agent.ModelClient`.
