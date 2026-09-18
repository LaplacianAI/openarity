---
title: Examples
weight: 5
---

Ten runnable agents live in [`sdk/agent/examples`](https://github.com/LaplacianAI/openarity/tree/main/sdk/agent/examples).
Every one of them runs with nothing installed and no key.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/sdk/agent
go run ./examples/tools
```

With no `OPENARITY_MODELS_BASE_URL` set they start a stub gateway in-process
that speaks the OpenAI streaming protocol. Only the model's judgement is
scripted — the pattern, the SSE accumulator and the tool dispatch are the real
ones, which is also why CI runs all of them on every push.

## What each one shows

| Example | What it shows |
| ----------------- | -------------------------------------------------------------------------- |
| `tools`           | one turn, one tool, streaming — read this first                             |
| `skills`          | two skills offered, one opened, and the body of the other never read        |
| `custom`          | a wrapper pattern written outside the SDK, enforcing a token ceiling        |
| `rewoo`           | the same dependent chain under ReAct and under ReWOO, side by side          |
| `mcp`             | an MCP server, in process, its tools reaching the loop as any other         |
| `reflection`      | a wrong first draft, and what catching it costs                             |
| `steering`        | a message sent to a run already in progress, and where it lands             |
| `steering-typed`  | typing at an agent while it works — `STEER_FROM_STDIN=1` to use your own keys |
| `steering-limits` | where steering stops: a plan already fixed, and a steer with no request left |
| `structured`      | a schema the model answers in, and a parser model turning prose into one      |

`examples/gateway` is not an example. It is the stub and the printing the
others share, and `make example` — which runs every one of them — skips it by
name.

## Reading the output

Each example prints the run as it happens, then a summary.

```text
[step 2]
  [90 in, 12 out]                     what this step cost
  → Skill({"name":"commit-style"})    the model asked for a skill
  ← Skill: # Commit style…            the body arrived
...
tokens   270 in (0 cached), 36 out
bodies   commit-style read 1 time(s), pdf-forms read 0 time(s)
```

That last line is the whole point of `skills`. Both skills spent their
description in the prompt; only the one the model asked for spent its body, and
the other was never read at all. Without the count you would have to take
progressive disclosure on trust.

`rewoo` runs one task twice and prints both:

```text
         turns  tools  tokens
react    3      2      939 in, 36 out
rewoo    2      2      819 in, 24 out

input tokens on the last turn: react 378, rewoo 295.
```

The stub prices a request by its size, so those numbers measure the context each
turn carried rather than a flat rate. ReWOO pays for its tool catalogue up front
and still comes out ahead, because ReAct re-sends everything before it on every
turn. The task is a dependent chain on purpose — find the latest release, then
count what was filed since it. Independent calls are not the case where the two
differ: a current model asks for those together in one turn, so ReAct pays one
model call for all of them and ReWOO saves nothing.

## Against a real gateway

The same three variables work for LiteLLM, OmniRoute, or a provider directly.

```sh
OPENARITY_MODELS_BASE_URL=http://127.0.0.1:20128/v1 \
OPENARITY_MODELS_API_KEY=sk-… \
OPENARITY_MODEL=cc/claude-opus-4-8 \
go run ./examples/skills
```

`deployment/` has a compose file for each gateway. The stub is scripted, so only
a real model tells you whether the prompt, the tool descriptions and the skill
descriptions actually work.

{{< callout type="info" >}}
`0 cached` against a real gateway usually means the prefix was too short rather
than that caching failed. Anthropic's minimum is 512 tokens for Opus 4.7 and
newer and 1,024 for Sonnet; a request under it is served without caching and
without an error. A one-sentence system prompt and two skill descriptions do not
reach it.
{{< /callout >}}
