---
title: Patterns
weight: 2
---

A pattern is the shape of the reasoning a run happens inside. `Runner.Run`
drives one; which one is a field on the spec, so the same runner serves all of
them.

| Constructor              | Streaming variant        | Model calls            |
| ------------------------ | ------------------------ | ---------------------- |
| `patterns.ReAct()`       | `ReActStreaming()`       | one per step           |
| `patterns.Plan()`        | `PlanStreaming()`        | one, then one per step |
| `patterns.ReWOO()`       | `ReWOOStreaming()`       | two, always            |
| `patterns.Reflection(n)` | `ReflectionStreaming(n)` | one, then two per cycle |

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

## Reflection

Answer, then ask whether the answer holds, and rewrite only if it does not.
[The published algorithm](https://agent-patterns.readthedocs.io/en/stable/patterns/reflection.html)
is four phases in a loop:

```text
output = generate(task)
for cycle in cycles:
    critique = reflect(task, output)
    if not needs_refinement: return output
    output = refine(task, output, critique)
```

The cycle count is a constructor argument — `patterns.Reflection(2)` — not a
field on the `Spec`. How many times to second-guess an answer is a deployment's
policy, the same reasoning that keeps a token ceiling out of the spec.

### The check is a boolean, not a sentence

The published pseudocode leaves `evaluate_reflection` undefined, and the obvious
implementation reads the critique for approval. That fails the first time a
model writes *"no major changes needed, though I would note…"*.

So the critique arrives as a tool call:

```json
{"needs_refinement": true, "critique": "Germany's capital is Berlin, not Paris."}
```

`needs_refinement` is what the loop branches on. A critique asking for changes
without saying which is refused rather than acted on — a rewrite with no reason
rewrites at random and costs a call to do it.

### Who gets tools

The generating phase is a full ReAct run and may use whatever the spec offers,
so the answer being judged can be one the model had to go and look up. Critique
and rewrite get none: judging an answer and improving it are reading tasks, and
a critic holding tools tends to go and redo the work rather than assess it.

With no tools offered at all this is byte-identical to the published algorithm —
a superset, not a deviation.

### What it costs

One call to answer, then two per cycle, and it stops early the moment the
critique approves. So `Reflection(2)` costs between two and five calls, and the
budget for the cycles is **reserved before generating**:

```go
spec.MaxSteps = in.Spec.MaxSteps - 2*cycles
```

Without that a tool-using generation phase spends the whole budget and the
critique never happens — a failure that looks like reflection quietly not
working rather than like an error.

`MaxSteps` below what the cycles need is refused at the start, not discovered
half way through a rewrite.

### What the caller gets back

One answer. The rewrite **replaces** the draft in `Result.Messages` rather than
following it, and the critique never enters the conversation at all — a caller
handing the messages back next turn would otherwise be giving the model two
answers to the same question and an argument between them.

{{< callout type="info" >}}
This is reflection, not [Reflexion](https://arxiv.org/abs/2303.11366). Reflection
iterates within one task and remembers nothing afterwards; Reflexion carries
what it learned across separate attempts. Only the first is implemented.
{{< /callout >}}

## Writing your own

`Pattern` is an interface, so a pattern is anything with a name and a `Run`.
The usual shape is not a new loop but a **wrapper** — take a shipped pattern,
apply your deployment's rule around it, and keep its name so nothing about the
spec has to know the wrapper is there.

That is the extension point the library exists for, and it has a page of its
own: [Writing a pattern](../custom-patterns).
