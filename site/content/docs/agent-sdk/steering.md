---
title: Steering a run
weight: 6
---

Saying something to an agent that has already started.

## The model is not listening

It feels as though a running agent is thinking, and could be interrupted. It
cannot. The model is closer to a vending machine: you push in the whole
conversation, it hands back one reply, and it is done. Between those two
moments it has no ears.

An agent is a loop your program drives around that:

```text
you ────► [whole conversation] ──► model
                                     │
                                     ▼
                          "run the search tool"
                                     │
              your program runs it ──┤
                                     ▼
            [conversation + tool answer] ──► model
                                     │
                                     ▼
                            "here's your answer"
```

Every arrow going *into* the model is your program assembling text. That is the
only moment new words can enter — so a steer is not delivered. It waits, and
rides the next request out.

## Using it

`Run` waits for the answer. `Start` hands back a handle while the run works:

```go
run := runner.Start(ctx, spec, msgs, endpoint, events)

go func() {
    for ev := range events {
        if call, ok := ev.(agent.ToolCallEvent); ok && wrongDirection(call) {
            run.Steer("the failure is in vault.go, stop reading tests")
        }
    }
}()

result, err := run.Wait()
```

`Run` is unchanged and still right when you only want the answer — it is
`Start(...).Wait()`.

## Where a steer lands

Not in a message of its own, usually. After a tool call the provider requires
the very next message to be that call's result; a user message wedged between
them is rejected before the model reads anything. But a tool result is
free-form text, so the steer goes on the end of it:

```text
carried by  a tool message, not one of its own:
    │ 12 files match
    │
    │ The user sent this while you were working:
    │ the failure is in vault.go, stop reading tests
```

With no tool call outstanding there is nothing to ride, and the steer becomes
an ordinary user message. Both are always legal at the moment a request is
assembled, which is why those are the two cases.

It is labelled on purpose. Unlabelled, a steer reads as something that was
always in the conversation — and "look at vault.go" arriving as ordinary
history is advice the model may weigh against its current plan, rather than an
instruction that just arrived from the person watching it work.

## No pattern implements this

Patterns reach the model through `ModelClient` and nothing else, so the runner
wraps that — the same seam it already uses to total usage without a pattern
knowing to try.

ReAct, plan-then-act, ReWOO, reflection and any pattern you write yourself are
steerable with no change, and a pattern cannot forget to support it. That is
the reason it lives there rather than in the loop: steering that each pattern
had to implement would be steering that two thirds of them got wrong.

## What it does not do

**It does not interrupt.** The tool in flight finishes. Cancel the context if
you want the run to stop; that is a different thing and it ends the run rather
than redirecting it.

**It cannot resurrect a finished run.** If the model has stopped calling tools,
there is no next request, and nothing can create one from outside a pattern.
Both halves of that are visible rather than silent:

- `Steer` returns `ErrRunFinished` once there is no request left to carry it.
- A steer sent during what turns out to be the final call comes back on
  `Result.UnappliedSteers`.

A steer that silently vanished would be indistinguishable from one the model
read and chose to ignore, which is the one outcome worth ruling out.

**It is not in the transcript.** `Result.Messages` is the conversation the
pattern built; the steer is added to the copy that goes to the provider. Hand
that slice back on the next turn and the steer is not replayed as something the
user said again.

## Seeing it work

```sh
go run ./examples/steering          # the mechanism: what carried the steer
go run ./examples/steering-typed    # type at an agent while it works
go run ./examples/steering-limits   # where it stops working
```

The first prints the message that carried the steer, and the count of
transcript messages containing it — which is zero.

The second is the shape most programs want: a person watching a run and saying
something to it. `STEER_FROM_STDIN=1` reads your keystrokes; without it a
scripted line goes through the same call, because `make example` inherits the
terminal and an example that always read stdin would hang the suite.

The third shows both limits — a ReWOO plan that was fixed a model call before
the steer existed, and a steer with no request left to carry it coming back on
`UnappliedSteers` rather than vanishing.
