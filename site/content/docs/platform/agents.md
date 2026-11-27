---
title: Agents and delegation
---

An agent is a model, a pattern for working through a task, instructions, and
the MCP servers and skills it is granted. Grants stay inside the team: an
agent cannot be given another team's server or skill.

## Defining one

An agent is a YAML or JSON file — the fields are the API's `AgentRequest` —
written with its grants in one step, so it never exists without what it was
meant to have:

```yaml
# triage.yaml
name: triage
# what it does and when to use it: this is how other agents find it
description: Sorts incoming issues and labels them
instructions: Read the issue. Label it. Say why.
model: {name: anthropic/claude-sonnet-5-5, max_tokens: 200000}
pattern: react
max_steps: 5
delegation: listed      # none (the default), team, or listed
delegates: [billing]    # read only with listed
web: true               # web_search and web_fetch; off unless set
browser: false          # the team's browser, to drive pages with; off unless set
workspace: false        # a shell and files in the conversation's sandbox; off unless set
schedule: false         # proposes schedules and watchers from a chat; off unless set
```

```sh
oa agents create platform -f triage.yaml \
    --mcp-server github=search_code --skill pdf-processing
oa runs start platform triage 'Sort the issues opened today.' --follow
```

`--mcp-server github` grants every tool the server offers;
`github=search_code,create_issue` grants only those.

| Field | What it is |
| --------------- | -------------------------------------------------------------- |
| `description`   | What it is for. The graph indexes it, so it is how the hub and other agents find this one |
| `pattern`       | The loop it works through a task with: `react`, `plan`, `rewoo` or `reflection` — `GET /teams/{id}/loops` lists what the brain runs. See [Patterns](../agent-sdk/patterns.md) |
| `max_steps`     | How many model turns one run may take                           |
| `token_budget`  | What one run may spend, input and output together, a token the provider read from its cache counting as a tenth of one. With a tenth left the run compacts its context; when one more call would not fit it is asked to answer with what it has. Past it the run fails, naming the budget, and keeps its latest summary as its output |
| `output_schema` | A JSON Schema the answer has to fit; `parser: true` has a second model fit it — see [Structured output](../agent-sdk/structured-output.md) |
| `steer_continuations` | How many times a run may carry on for a message that arrived as it finished |
| `delegation`    | Whom it may hand work to: `none`, any agent in the `team`, or the `listed` `delegates` |
| `web`           | Gives it `web_search` and `web_fetch` — see [Web tools](web.md)  |
| `browser`       | Lets it drive the team's browser — see [A browser](web.md#a-browser) |
| `workspace`     | Gives it `Bash`, `Read`, `Write`, `Edit`, `Glob` and `Grep` in a sandbox room of the conversation's — see [Workspaces](workspaces.md) |
| `schedule`      | Gives it `schedule` and `watch` in a chat: it proposes a schedule or a watcher that runs as the person, answering in that chat, or in their direct messages on a chat channel. On the dashboard it answers in a conversation of its own. Nothing it asks (a new one, a change, a delete, a resume or a run now) happens until the person replies, in the same conversation and after the agent has answered, with the code the agent gives them; it must confirm before it reads anything else in that turn, and an ask left unanswered lapses in an hour. Only a pause acts at once, and so does deleting a proposal that never ran. The hub has them whatever this says; the team's settings can turn them off for every agent, and a person without `schedule:write` never has them |

A `rewoo` agent plans every call before making one, so it cannot search for
tools midway. When its granted tools are too many to send, it plans with the
tools the team's [graph](../concepts/why-a-graph.md) picks for the task, at most
15, within what the agent can reach. When the team has no embedding model, or
the graph picks none of them, it is sent every tool. The other patterns search
instead.

## Changing one

A replace is whole: a grant the new definition does not name is revoked, and a
field it leaves out goes back to its default. To change one thing, start from
what is there:

```sh
oa agents get platform triage -o yaml > triage.yaml
# edit triage.yaml
oa agents replace platform triage -f triage.yaml
```

Seeing a team's agents needs only to be in the team; defining, replacing or
deleting one needs `agent:write`. Deleting an agent takes its grants with it;
the servers and skills it was granted stay.

## Delegation

An agent whose `delegation` is `team`, or `listed` with at least one delegate,
has a `delegate` tool; one set to `none` has not. It hands a task to another
agent as a child run, built from that agent's own definition, so delegation
never widens what an agent can reach. Delegation stops three levels down: a
run that deep is given no `delegate`. The team's [hub](hub.md) may delegate to
any agent it finds in the graph.

A child starts from nothing, on its own agent's `token_budget`, with its own
context and its own compaction; the parent spends only what it takes to read
the child's report. Tokens are not shared across a task. What a task does
share is a limit for a runaway: at most 50 runs and six hours, not counting
time spent waiting for a person.

## Not built

- **A run's tools are its agent's grants**, not chosen per task by walking the
  graph; see [Why a graph](../concepts/why-a-graph.md).
- **A new agent cannot be found for up to a minute**, until the graph's drain
  takes it in.
- **The `code` pattern** is a name the runner refuses: running model-written
  code needs a sandbox that is not written.
