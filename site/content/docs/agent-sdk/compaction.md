---
title: Compaction
---

Keeping a long run's context small, reading a large result back in parts, and
finishing before the budget is gone.

## Why a long run outgrows itself

Every step of a loop sends the whole conversation again. Step forty carries
steps one to thirty-nine, so a run that reads diffs and files spends
quadratically, and one large tool result is resent on every step after it.
A run with a token budget hits it long before its model's context window.

Compaction keeps the request small enough that the budget pays for work
rather than for resending it. It is off unless a spec asks for it.

## Using it

```go
spec := agent.Spec{
	Model:    agent.ModelRef{Name: model, MaxTokens: 4096},
	Pattern:  agent.PatternReAct,
	Tools:    tools,
	MaxSteps: 40,
	Compaction: &agent.Compaction{
		AfterMessages:    100,     // compact past this many messages
		AfterInputTokens: 100_000, // or when the next request would read more
	},
}
```

Before each request the run estimates its size: what the last call read,
measured, plus what was added since. Past either threshold it compacts first,
cheapest way first:

1. **Clear.** Tool results older than the latest turns become a one-line
   handle. No model call. If that is enough, it stops here.
2. **Summarise.** The older turns are replaced by a summary the run's own
   model writes, with the run's own system prompt, tools and conversation plus
   one instruction, so a provider's prompt cache covers all but that
   instruction.

What the model is sent afterwards:

```text
[the task, word for word]
[This run was compacted. A summary of its earlier part follows …
 What the user said in that part, word for word: …
 References from that part: https://…/pull/49, apps/brain/…/report.go, call_7 (get_diff: 1.8 MiB) …
 Skills you loaded in that part, as they were: …]
[the latest turns, about 15,000 tokens, word for word]
```

The transcript keeps every message, and the summary is written into it as one
more, where the turns it stands for end:

```text
[the task]
[the turns that were folded, word for word]
[the summary: a user message, marked with Message.Compaction]
[the latest turns, and everything after]
```

Only the model sees the shorter form, so a transcript stays a complete record
of what happened and of what the model was told about it.

## What survives a summary

A summary is the model's own words, and compression drops identifiers first.
So three things are kept beside it, without a model:

| Kept | How |
| --- | --- |
| Every message from the user, steers included | word for word |
| References: URLs, `#123`, uuids, paths, and a handle for every large result | extracted, appended, never summarised |
| Every skill the run loaded | its body, word for word |

A call and its results always stay together: the part kept word for word
never starts on a tool result.

## Large results

A tool result over 100 KiB is shown as its first 8 KiB, its last 4 KiB, and a
line saying how to read the rest. The transcript keeps it whole. Those three
sizes are the defaults; a run sets its own, and a tool its own over that:

```go
spec.Compaction = &agent.Compaction{
	AfterMessages: 100,
	Preview:       agent.Preview{Over: 64 << 10}, // every tool: past 64 KiB, its first 8 KiB and last 4 KiB
}
fetch := agent.Tool{
	Name:    "web_fetch",
	Preview: agent.Preview{Over: 32 << 10, Head: 24 << 10, Tail: 8 << 10}, // a page: past 32 KiB, its two ends
	// …
}
```

A zero field takes the run's value, and the run's takes the default. A
preview whose head and tail do not fit under where it starts is refused when
the run starts, naming the tool. Neither a `read_output` result nor a skill is
ever previewed: the first is already capped, and the second is kept word for
word. A compacting run has one more tool for the rest:

```json
{"call": "call_7", "from_line": 1200, "lines": 200}
{"call": "call_7", "match": "func checkDelegation"}
```

`read_output` reads the whole result from the run's own transcript, by its call
id: a line range, or the lines a regular expression (RE2) matches, with their
numbers, under a line naming the tool they came from. One read returns at most
about 25,000 tokens, and a line longer than 4 KiB, a minified file's, is read in
parts. It works after a compaction and after a resume.

## Finishing before the budget is gone

A run can be asked to finish:

```go
run := runner.Start(ctx, spec, msgs, endpoint, events)
// later, when what it has spent says one more call would not fit:
run.WrapUp("Your token budget is nearly spent. Stop calling tools and answer now with what you have.")
```

The text reaches the model on its next request, as the caller's words rather
than the user's. From then on every tool refuses with the same words. The tools
stay listed, because a provider may reject a conversation holding tool calls
when no tools are defined, so the model answers instead.

`run.CompactNow()` compacts before the next request whatever the thresholds.
Asked when a budget is nine-tenths spent, it makes the last calls cheap enough
that a run can work on before it has to wrap up.

The summary call's tokens are the run's: they arrive as a `UsageEvent` with
`Summary` set, so a budget counts them, and a caller reading the next call's
size from usage knows to skip this one.

## Resuming

Nothing to keep beside the transcript. Hand a run its messages again, from a
`Store` or from `Result.Messages`, and it carries on from the latest summary in
them with no model call:

```go
result, _ := runner.Run(ctx, spec, msgs, endpoint, events)
// later, or in another process:
more := append(result.Messages, agent.Message{Role: agent.RoleUser, Content: …})
runner.Run(ctx, spec, more, endpoint, events)
```

The summary records a hash of the messages before it. A transcript edited
before its summary no longer starts with what the summary stands for, so the
run drops it and compacts again as it needs. A pattern never sees a summary
message, and a run without `Compaction` sends one as the user message it
reads as.

Each compaction also emits a `CompactedEvent`, for a trace: why it happened,
what it cleared and folded, the sizes before and after, and the summary.

## When a provider refuses

When a gateway answers a request with `context_length_exceeded`, the run
compacts and tries once more. A second refusal is returned. A model with a
small context window carries on rather than failing, without the SDK knowing
its window.

## What it does not do

- **It does not raise a budget.** Spent tokens stay spent; compaction makes the
  next ones fewer.
- **It is not Anthropic's server-side compaction.** The SDK reaches models
  through an OpenAI-compatible endpoint, and a provider's signed compaction
  block does not survive a gateway.
- **It does not prune without a model.** Clearing replaces results with
  handles; it never drops a turn without a summary standing in for it.

## Seeing it work

```sh
go run ./examples/compaction
```

```text
input per call   405 → 3.8k → 3.9k → 18.6k → 14.9k → 15.0k → 29.3k → 29.4k
the large result 280 KiB in the transcript; the model was shown 12 KiB of it,
                 and read back only the line it needed
compacted        tokens: 0 message(s) folded, 2 result(s) cleared, about 32.5k → 14.4k tokens
compacted        messages: 8 message(s) folded, 1 result(s) cleared, about 29.1k → 14.6k tokens
wrap-up          asked at 86.2k tokens spent
refused          1 tool call(s) after the wrap-up
in the transcript a summary, at message 9 of 17
resumed from it  1 model call(s), 0 of them a summary; its first request opened
                 with the task and the summary it carried: true
```

The numbers are measured from the requests the model was sent, not asserted.
