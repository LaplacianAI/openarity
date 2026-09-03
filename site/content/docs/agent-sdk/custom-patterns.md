---
title: Writing a pattern
weight: 3
---

A pattern is the shape of the reasoning a run happens inside. `Runner.Run`
drives one; everything about *how* a task is approached lives in it.

This is the extension point that matters. ReAct, plan-then-act, ReWOO and
reflection ship
with the library because they come from the literature and everyone wants them.
Your deployment's rules — a spending ceiling, an allowlist, a retry policy, an
approval gate — do not belong in a library, because they change when you change
your mind. What the library ships is the interface that lets you write them.

## Where a pattern sits

```text
  your code
      │  Runner.Run(ctx, spec, msgs, endpoint, events)
      ▼
  Runner ──── folds skills into the spec
      │  ──── builds a client for the endpoint
      │  ──── wraps it in the usage counter
      ▼
  Pattern.Run(ctx, Input)
      │
      ▼
  Input.Model ──── Complete / Stream
```

The runner does three things before your pattern is called, so you never do
them: skills become a tool and a listing, the endpoint becomes a client, and
that client is wrapped so every call is counted.

`Input.Model` is your **only** handle on the outside world. There is no global,
no ambient client, and nothing to reach for. That is what makes usage counting
exact and what makes a pattern testable with a fake.

## The contract

```go
type Pattern interface {
	Name() PatternName
	Run(ctx context.Context, in Input) (Result, error)
}

type Input struct {
	Spec     Spec           // model, system prompt, tools, skills, MaxSteps
	Messages []Message      // the conversation so far
	Model    ModelClient    // Complete and Stream; already counted
	Events   chan<- Event   // may be nil
}

type Result struct {
	Output   string         // the final text
	Messages []Message      // what you were given, extended
	Usage    Usage          // set by the runner; do not fill it in
	Steps    int
}
```

Two of those need saying out loud.

**`Result.Messages` is what you were given, extended.** Not just what you
produced. A caller hands it back on the next turn and the agent has memory, so
dropping the prior conversation silently breaks multi-turn for everyone
downstream.

**`Result.Usage` is not yours to fill in.** The runner overwrites it with the
count taken at the client, on the error path too. Totalling it yourself is
wasted work that reads as required to whoever copies your pattern next.

Emit through `in.Emit`, never by sending on `in.Events` directly — it is
nil-safe and it gives up when the context is cancelled, which a bare send
does not:

```go
func (in Input) Emit(ctx context.Context, e Event) {
	if in.Events == nil {
		return
	}
	select {
	case in.Events <- e:
	case <-ctx.Done():
	}
}
```

## A pattern from scratch

This one answers, then asks the model to check its own answer — two calls, no
tools. It is small enough to read in one go and complete enough to run.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
)

// Critique answers, then has the model check what it just said.
func Critique() agent.Pattern { return critique{} }

type critique struct{}

func (critique) Name() agent.PatternName { return agent.PatternCustom }

func (c critique) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	if in.Model == nil {
		return agent.Result{}, errors.New("no ModelClient was given, so there is nothing to call")
	}
	if in.Spec.MaxSteps < 2 {
		return agent.Result{}, fmt.Errorf("%w: this pattern answers and then checks, so it needs two",
			agent.ErrNoMaxSteps)
	}

	msgs := slices.Clone(in.Messages)
	result := agent.Result{Messages: msgs}

	in.Emit(ctx, agent.StepEvent{Step: 1})
	first, err := c.ask(ctx, in, in.Spec.System, msgs)
	if err != nil {
		return result, fmt.Errorf("answering: %w", err)
	}
	result.Steps = 1
	msgs = append(msgs, first.Message)
	result.Messages = msgs
	result.Output = first.Message.Text()

	if first.Finish == agent.FinishLength {
		return result, agent.ErrTruncated
	}

	// Cloned before appending. in.Spec.System belongs to the caller, and a
	// slice with spare capacity would let this write into their array.
	checking := append(slices.Clone(in.Spec.System), agent.Content{
		Type: agent.ContentText,
		Text: "Check the answer above. If it is wrong or incomplete, replace it. " +
			"If it is right, repeat it unchanged.",
	})

	in.Emit(ctx, agent.StepEvent{Step: 2})
	second, err := c.ask(ctx, in, checking, msgs)
	if err != nil {
		// The first answer is still worth returning. A caller that got half a
		// run wants the half it paid for.
		return result, fmt.Errorf("checking: %w", err)
	}
	result.Steps = 2
	msgs = append(msgs, second.Message)
	result.Messages = msgs
	result.Output = second.Message.Text()

	if second.Finish == agent.FinishLength {
		return result, agent.ErrTruncated
	}
	return result, nil
}

func (critique) ask(
	ctx context.Context, in agent.Input,
	system []agent.Content, msgs []agent.Message,
) (agent.Response, error) {
	resp, err := in.Model.Complete(ctx, agent.Request{
		Model:    in.Spec.Model,
		System:   system,
		Messages: msgs,
	})
	if err != nil {
		return agent.Response{}, err
	}
	if text := resp.Message.Text(); text != "" {
		in.Emit(ctx, agent.TextEvent{Delta: text})
	}
	in.Emit(ctx, agent.UsageEvent{Model: in.Spec.Model.Name, Usage: resp.Usage})
	return resp, nil
}

func main() {
	runner, err := agent.New(openaicompat.Factory(), Critique())
	if err != nil {
		panic(err)
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: "gpt-4o-mini"},
		Pattern:  agent.PatternCustom,
		System:   agent.System("You are a helpful assistant. Answer in a sentence or two."),
		MaxSteps: 2,
	}

	msgs := []agent.Message{{
		Role: agent.RoleUser,
		Content: []agent.Content{{
			Type: agent.ContentText,
			Text: "Which is heavier, a kilo of steel or a kilo of feathers?",
		}},
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

## Registering and selecting are different jobs

`Name()` is the key that goes **into** the registry. `Spec.Pattern` is the key
you look up **with**. They hold the same string and answer different questions,
which is worth spelling out because the redundancy looks accidental and is not.

```text
agent.New(factory, ReAct(), Plan(), Critique())   once, at startup
    byName[p.Name()] = p                          "which pattern is this?"

Runner.Run(ctx, spec, …)                          once per run
    r.patterns[spec.Pattern]                      "which one does this run want?"
```

They have different owners and different lifetimes. **Registration is a
deployment decision**: which reasoning shapes this process permits, fixed when
it starts. **Selection is a task decision**: which one this particular run gets.
One agent can answer a cheap lookup with ReAct and a dependent chain with
ReWOO, from the same runner, because only the spec changed.

That also means the spec can be **data**. `pattern: "rewoo"` is a column, or a
field in a JSON body; a `Pattern` is a Go value holding closures and cannot come
out of a database row. The registry is the one place that turns the first into
the second. Remove it and every caller has to do that mapping itself — you have
not deleted the registry, you have copied it.

The registry is therefore also an allowlist. Asking for something unregistered
is refused before anything else happens:

```text
err:               no pattern named "rewoo" is registered; this runner has react
gateway reached:   0 times
```

The lookup is the first statement in `Run` — before skills are folded in and
before the client is built — so a mismatch costs nothing and reaches nothing.
Leaving `Spec.Pattern` empty is refused the same way, with a message that names
the field rather than reporting a failed lookup for a pattern called `""`.

The honest cost of selecting by string is that this is a **runtime** failure.
`agent.PatternReWOO` compiles perfectly against a runner that never registered
it. That is the price of the caller holding a name instead of a value, and it
is why the error lists what *is* registered instead of only saying no.

## A wrapper, which is usually what you want

Most policies are not a new way of reasoning. They are a rule applied to an
existing one, and a wrapper says that directly: keep the inner pattern's
**name**, so a caller asks for `react` and your rule arrives underneath without
anything about the spec knowing the wrapper exists.

This one hides every tool that is not on an allowlist.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/agent/models/openaicompat"
	"github.com/LaplacianAI/openarity/sdk/agent/patterns"
)

// Restricted wraps any pattern and hides every tool not named in allowed.
func Restricted(inner agent.Pattern, allowed ...string) agent.Pattern {
	return restricted{inner: inner, allowed: allowed}
}

type restricted struct {
	inner   agent.Pattern
	allowed []string
}

// The wrapped pattern's name, not one of its own. Ask for "react" and this
// deployment's allowlist comes with it.
func (r restricted) Name() agent.PatternName { return r.inner.Name() }

func (r restricted) Run(ctx context.Context, in agent.Input) (agent.Result, error) {
	kept := make([]agent.Tool, 0, len(in.Spec.Tools))
	for _, t := range in.Spec.Tools {
		if slices.Contains(r.allowed, t.Name) {
			kept = append(kept, t)
		}
	}
	if len(in.Spec.Tools) > 0 && len(kept) == 0 {
		return agent.Result{}, fmt.Errorf(
			"none of the %d tools offered are on this allowlist", len(in.Spec.Tools))
	}

	// `in` is a copy and so is its Spec, so assigning the field cannot reach
	// the caller. Building a new slice rather than filtering in place is what
	// keeps that true — writing to in.Spec.Tools[0] would reach them.
	in.Spec.Tools = kept
	return r.inner.Run(ctx, in)
}

func main() {
	runner, err := agent.New(openaicompat.Factory(),
		Restricted(patterns.ReAct(), "clock"),
	)
	if err != nil {
		panic(err)
	}

	spec := agent.Spec{
		Model:    agent.ModelRef{Name: "gpt-4o-mini"},
		Pattern:  agent.PatternReAct,
		System:   agent.System("You are a helpful assistant. Answer in a sentence or two."),
		MaxSteps: 4,
		Tools: []agent.Tool{
			{
				Name:        "clock",
				Description: "The current time, as an RFC 3339 timestamp.",
				Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Invoke: func(context.Context, json.RawMessage) (string, error) {
					return time.Now().Format(time.RFC3339), nil
				},
			},
			{
				Name:        "delete_everything",
				Description: "Not on the allowlist, so the model never sees it.",
				Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Invoke: func(context.Context, json.RawMessage) (string, error) {
					return "", fmt.Errorf("this should never be reached")
				},
			},
		},
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

The same shape covers a spending ceiling, a retry, an approval gate, or logging
every step somewhere. [`examples/custom`](https://github.com/LaplacianAI/openarity/tree/main/sdk/agent/examples/custom)
is a runnable wrapper that enforces a token ceiling by watching `UsageEvent` as
the run happens and cancelling the context the moment it is crossed — because
afterwards the tokens are already spent.

## The rules

Every one of these cost somebody a debugging session.

**Clone before you append to anything in the spec.** `in.Spec.System` and
`in.Spec.Tools` belong to the caller. `append` to a slice with spare capacity
writes into their array, and the corruption shows up in a *different* run.
`slices.Clone` first, always. This is easy to get wrong in a way tests miss:
`agent.System("…")` returns a slice with `len == cap`, so `append` reallocates
and a broken pattern passes. Build the fixture with spare capacity if you want
the test to mean anything.

**Guard `MaxSteps` before the first call.** Zero means a looping model runs
until the context dies. Return `agent.ErrNoMaxSteps`, wrapped if your pattern
needs more than one.

**Return the partial result on the error path.** A run that failed at step four
still did three steps' work and still spent the money. `return result, err`,
not `return agent.Result{}, err`.

**Check `Finish == agent.FinishLength` before you act on a turn.** A turn
truncated at `MaxTokens` can carry a half-written tool call, and dispatching it
sends malformed arguments to something real.

**A failed tool is a result, not a failure.** Put the error text back in the
conversation as the tool's output and let the model recover. A run that dies on
the first bad argument is worse than one that reads "repository not found" and
tries again.

**Check `ctx.Err()` after a tool call.** A cancelled context does not stop a
tool that already started, and the loop should not continue as though nothing
happened.

**Renumber the steps if you wrap.** A wrapper that runs a phase of its own and
then delegates will emit `StepEvent{1}` twice unless it shifts the inner
pattern's numbering. `patterns.Plan` does exactly this.

**Do not touch `Result.Usage`.** The runner sets it. Yours will be overwritten
and the line you wrote will be copied by the next person as though it mattered.

## Testing one

A pattern's only outside contact is `Input.Model`, so a fake is four lines and
needs no server:

```go
type scripted struct {
	replies []agent.Response
	calls   []agent.Request
}

func (s *scripted) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	s.calls = append(s.calls, req)
	next := s.replies[0]
	s.replies = s.replies[1:]
	return next, nil
}
```

Keeping the requests is the point. Most of what is worth asserting is about
what the pattern *sent*: that the prior conversation opened every request and
not only the first, that the planning call was offered no tools, that the
second call carried the first call's answer.

Then break each guard and watch a test fail. A guard nobody has broken is a
guard nobody has tested — every pattern in this repository has had its `nil`
model check, its `MaxSteps` check and its clone removed in turn to prove the
suite notices.
