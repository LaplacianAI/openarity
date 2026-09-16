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

`Run` waits for the answer. `Start` hands back a handle while the run works, and
that handle is the whole API:

```go
run := runner.Start(ctx, spec, msgs, endpoint, nil)

go func() {
    line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
    if err := run.Steer(strings.TrimSpace(line)); err != nil {
        fmt.Fprintln(os.Stderr, "too late:", err)
    }
}()

result, err := run.Wait()
```

Type while it works and the line goes out with the next model request. Nothing
else changes: same `Spec`, same pattern, same `Result`. `Steer` is safe from any
goroutine and returns `ErrRunFinished` rather than swallowing a line that
arrived after the end.

`Run` is unchanged and still right when you only want the answer — it is
`Start(...).Wait()`.

## Steering on what the run does

A steer does not have to come from a person. Watch the event stream and send one
when the run goes somewhere you did not want:

```go
events := make(chan agent.Event, 64)
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

## Where a steer lands

In a message of its own, from the user, at the point in the conversation where
it arrived:

```text
carried by  a user message of its own, at index 4 of 8:
    │ The user sent this while you were working:
    │ the failure is in vault.go, stop reading tests

still last  a tool message
```

Two things about that placement are load-bearing, and both were learned by
getting them wrong against a real model.

**It is not appended to the tool result.** A provider requires the message
straight after a tool call to be that call's result — but only *straight
after*. A user message following the results is legal, and it matters: text
added to tool output reads as something the tool said, which is not who said
it.

**It does not follow the end of the conversation.** Each steer remembers how
many messages existed when it arrived and is re-inserted at that index on every
later request. Keeping it last instead — so the model always sees it as the
most recent thing said — makes the model treat it as a brand-new instruction
every turn and never conclude: five steps, four tool calls, no answer. Pinned
to its index, the last thing the model reads is the tool result, and it can
stop.

It is labelled on purpose. Unlabelled, a steer reads as something that was
always in the conversation — and "look at vault.go" arriving as ordinary
history is advice the model may weigh against its current plan, rather than an
instruction that just arrived from the person watching it work.

Timing matches Claude Code, which queues a message typed mid-run and flushes it
at the next pause between tool calls rather than into a tool already running.

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

**It does not reset `MaxSteps`.** A steer arriving on the last permitted step
gets one step to change course, and a run at its limit ends at its limit —
`UnappliedSteers` says so. Claude Code is stricter still: a message queued when
`maxTurns` is reached stays queued and is never delivered. Extending the budget
would also mean the pattern re-reading `MaxSteps` each iteration, which is the
pattern cooperation this design exists to avoid.

## Seeing it work

```sh
go run ./examples/steering          # the mechanism: what carried the steer
go run ./examples/steering-typed    # type at an agent while it works
go run ./examples/steering-limits   # where it stops working
```

The first prints the message that carried the steer, its index, and what is
last in the request instead — the two facts that decide whether the model can
finish. `Result.Messages` carries the steer too, so the transcript you hand
back next turn records that the person spoke.

The second is the shape most programs want: a person watching a run and saying
something to it. `STEER_FROM_STDIN=1` reads your keystrokes; without it a
scripted line goes through the same call, because `make example` inherits the
terminal and an example that always read stdin would hang the suite.

The third shows both limits — a ReWOO plan that was fixed a model call before
the steer existed, and a steer with no request left to carry it coming back on
`UnappliedSteers` rather than vanishing.
