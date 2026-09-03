# Examples

Each runs end to end with nothing installed: with no `OPENARITY_MODELS_BASE_URL`
set they start a stub gateway in-process that speaks the OpenAI streaming
protocol, so the pattern, the accumulator and the tool dispatch are all real.

```sh
go run ./examples/tools
go run ./examples/skills
go run ./examples/custom
go run ./examples/rewoo
go run ./examples/mcp
```

Or run every one of them:

```sh
make example
```

| Example    | What it shows                                                        |
| ---------- | -------------------------------------------------------------------- |
| `tools`    | one turn, one tool, streaming — read this first                       |
| `skills`   | two skills offered, one opened, and the body of the other never read  |
| `custom`   | a wrapper pattern written outside the SDK, enforcing a token ceiling     |
| `rewoo`    | the same dependent chain under ReAct and under ReWOO, side by side    |
| `mcp`      | an MCP server, in process, its tools reaching the loop as any other   |
| `gateway`  | not an example: the stub and the printing the others share            |

## Against a real gateway

Both take the same three variables, so the same command works for LiteLLM,
OmniRoute, or a provider directly:

```sh
OPENARITY_MODELS_BASE_URL=http://127.0.0.1:20128/v1 \
OPENARITY_MODELS_API_KEY=sk-… \
OPENARITY_MODEL=cc/claude-opus-4-8 \
go run ./examples/skills
```

`deployment/` has a compose file for each gateway; see the deployment README for
which port each one listens on.

## Reading the output

```text
[step 2]
  [90 in, 12 out]                     what this step cost
  → Skill({"name":"commit-style"})    the model asked for a skill
  ← Skill: # Commit style…            the body arrived
...
tokens   270 in (0 cached), 36 out
bodies   commit-style read 1 time(s), pdf-forms read 0 time(s)
```

`rewoo` runs one task twice. The task is a chain — find the latest release, then
count what was filed since it — because that is the case where the two patterns
differ. Independent calls are not: a current model asks for those together in
one turn, so ReAct pays one model call for all of them and ReWOO saves nothing.

```text
         turns  tools  tokens
react    3      2      939 in, 36 out
rewoo    2      2      819 in, 24 out

input tokens on the last turn: react 378, rewoo 295.
```

The stub prices a request by its size, so those numbers measure the context each
turn carried rather than a flat rate. ReWOO pays for its tool catalogue up front
and still comes out ahead, because ReAct re-sends everything before it on every
turn.

`custom` wraps `patterns.Plan()` in a deployment's own spending rule. The ceiling
is enforced by watching `UsageEvent` as the run happens rather than by adding
up afterwards, when the tokens are already spent — so it cuts the run mid-flight
and overshoots by at most one step.

The last line is the point of the `skills` example. Both skills spent their
description in the prompt; only the one the model asked for spent its body, and
the other was never read at all.

`0 cached` against a real gateway usually means the prefix was too short rather
than that caching failed. Anthropic's minimum is 512 tokens for Opus 4.7 and
newer, 1,024 for Sonnet, and a request under it is served without caching and
without an error. A one-sentence system prompt and two skill descriptions do not
reach it.
