---
title: The hub
---

Every team has a front door: a run that names no agent goes to the team's hub.
It is the built-in orchestrator unless the team chooses one of its own agents,
and either way it can plan, find the agent that keeps what a task needs, and
hand it the work.

```sh
oa runs start platform 'Reconcile payment REF-9 against our open invoices.'
```

## The built-in orchestrator

A team with no hub of its own gets the orchestrator. It runs on the team's
model, so it cannot run until one is set:

```sh
oa teams settings set platform --model anthropic/claude-sonnet-5-5 --max-tokens 2048
```

Its tools:

| Tool | What it does |
| ------------ | -------------------------------------------------------------------- |
| `plan`       | Keeps a short plan for the run and updates it as steps finish        |
| `graph`      | Searches the team's [graph](../concepts/why-a-graph.md) for agents, tools and skills |
| `delegate`   | Hands a task to an agent it found, as a child run of its own         |
| `web_search` | Searches the web through the team's provider — see [Web tools](web.md) |
| `web_fetch`  | Reads one page, as markdown — see [Web tools](web.md)                |

Agents are found through the graph and listed nowhere else, so a team whose
graph has no embedding model gets a hub with no `delegate`: it is told the
team cannot be searched, and answers alone rather than guessing names.

## Choosing your own hub

Any agent can be the hub. As the hub it has `delegate`, `plan` and the web
tools whatever its own settings say; run as itself, it has only what it was
given.

```sh
oa teams settings set platform --hub triage
oa teams settings set platform --no-hub      # back to the orchestrator
```

Deleting the agent that is the hub returns the team to the orchestrator.

## What a delegate gets

A child run is built from its own agent: its model, its pattern, its grants.
The hub's reach is not passed down, so handing work to an agent never gives
it a server, a skill or the web it was not granted. The trace of the parent
shows each delegation, and the child's own events are a run of their own:

```sh
oa runs events platform <run>
```

## Not built

- **A new agent cannot be found by the hub for up to a minute.** Agents reach
  the graph through a drain that ticks once a minute.
- **`oa runs start <team> <agent>` with no input asks the hub**, treating the
  agent's name as the input.
- **Saving settings without the hub clears it.** `PUT /teams/{id}/settings` is
  whole, so a client that leaves `hub` out returns the team to the
  orchestrator; `oa teams settings set` reads first and keeps it.
- **Renaming the hub agent can fail the next settings save**, because settings
  name the hub by name.
