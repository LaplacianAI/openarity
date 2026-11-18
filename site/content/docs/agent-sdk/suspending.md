---
title: Waiting on a tool call
---

Stopping a run at a tool call until something outside it is ready, and carrying
on from exactly there.

## A run cannot wait inside a tool

Some calls should not be made yet. An email should be approved before it goes;
a deploy waits for a job to finish; a payment waits for the customer to confirm.
The obvious place to wait is inside the tool, and it is the wrong one: the tool
holds the run, the run holds a connection and a goroutine, and if the answer
takes an hour, or the process restarts, the run is lost.

So the tool does not wait. It says *not now*, and the run stops cleanly:

```text
turn:  assistant → [find_invoice, send_email]
         find_invoice ─────► result kept
         send_email   ─────► agent.Suspend("waiting for approval")
                             nothing after it is made

Run returns  (result, err)       errors.Is(err, agent.ErrSuspended)
             result.Messages     ends in that turn, send_email still open
```

Later, once the answer is in, the same transcript is run again. Before the model
is asked anything, the turn is finished: the open call is made again, with the
**call id it had**, so the tool can look up what was decided.

```text
Run(…, result.Messages, …)
   send_email (same call id) ──► approved? ──► "sent"
   then the model, once:        "Sent the reminder for invoice 1042."
```

## Using it

A tool suspends by returning `agent.Suspend`, and keys whatever it is waiting on
by `agent.ToolCallID`:

```go
Invoke: func(ctx context.Context, args json.RawMessage) (string, error) {
    id := agent.ToolCallID(ctx)
    switch approvals.Lookup(id) {
    case "approved":
        return mailer.Send(args)
    case "denied":
        return "", errors.New("a person declined this email; it was not sent")
    default:
        approvals.Ask(id, args) // a table, a Slack thread, a page with two buttons
        return "", agent.Suspend("waiting for someone to approve the email")
    }
},
```

The caller sees the suspension as an error, keeps the transcript, and runs it
again when it is time:

```go
result, err := runner.Run(ctx, spec, msgs, endpoint, events)
if errors.Is(err, agent.ErrSuspended) {
    save(result.Messages) // or let Spec.Session keep it
    return
}

// …an hour, or a restart, later:
result, err = runner.Run(ctx, spec, load(), endpoint, events)
```

With `Spec.Session` set the transcript is already saved when `Run` returns, as
it is for any other ending, so the second run can be in another process.

## What is kept, and what is not made

- **Calls before the suspended one** keep their results, and anything they
  attached stays with the turn.
- **The suspended call** gets no result. Nothing about the suspension is written
  for the model to read.
- **Calls after it** are not made. They are made on resume, in order.
- **A cancelled context wins.** A run cancelled while a tool suspends ends as
  cancelled, not suspended.
- **The resume is not a step.** Finishing the turn calls no model and counts no
  step; the model is asked once, after every call has its result.

## Which patterns can wait

| Pattern | On a suspension |
| --- | --- |
| ReAct | stops, and finishes the turn on resume |
| Plan | stops; on resume it does not plan again, and finishes the turn |
| Reflection | as ReAct, which it starts with |
| ReWOO | **cannot wait**: it plans every call before making one and keeps nothing between them, so the run ends with `patterns.ErrCannotWait`, which does not match `ErrSuspended` |

Wrapping tools is fine: `call_tool`, the way deferred tools are reached, passes a
suspension through unchanged.

## Open calls are made, not refused

The resume is general: any transcript that ends with calls left unanswered has
them made before the model is called. That is what a suspension needs, and it
also applies to a run that crashed in the middle of a tool. If a call with a side
effect must not run twice, close it before resuming, with a tool result of your
own that says what is known:

```go
msgs = append(msgs, agent.Message{
    Role: agent.RoleTool, ToolCallID: open.ID,
    Content: []agent.Content{{Type: agent.ContentText,
        Text: "The run stopped during this call; it is not known whether it was made."}},
})
```

## Try it

```sh
cd sdk/agent
go run ./examples/approval
```

```text
── first run
  → find_invoice({"number":"1042"})
  ← find_invoice: invoice 1042: 1,200 EUR, due 2026-10-31, billing@acme.exampl…
  → send_email({"to":"billing@acme.example","subject":"Invoice 1042 is overdue"})

suspended  the run is suspended on a tool call: waiting for someone to approve the email

approver   send_email {"to":"billing@acme.example",…}? yes

── second run, the same transcript
  → send_email({"to":"billing@acme.example","subject":"Invoice 1042 is overdue"})
  ← send_email: sent
[step 1] Sent the reminder for invoice 1042 to billing@acme.example.

calls    find_invoice made 1 time(s); send_email asked 2 time(s) for [call_1_2 call_1_2], sent 1
```

The last line is the point: the lookup is not made again, and the email is asked
about twice under one call id and sent once.
