---
title: Platform
---

The `brain` service, `oa` the CLI, and the inbound gateway they share.

<div class="grid cards" markdown>

  - **[Quick start](quick-start.md)**: A brain against a local Postgres, in about five commands.
  - **[Configuration](configuration.md)**: Every environment variable, its default, and whether it is used yet.
  - **[oa, the CLI](../reference/cli.md)**: Every command and subcommand, and the flags that apply to all of them.
  - **[HTTP API](../reference/http-api.md)**: Every endpoint the brain serves, and how a caller authenticates.

</div>

## What runs today

The brain runs agents. A run starts from `oa runs start` or the HTTP API,
executes inside `sdk/agent` against the team's model, and streams every step
back as an event; the dashboard shows each run's trace. A run also starts from
a message on a channel.

- **Agents**: a model, a reasoning pattern (ReAct, plan-then-act, ReWOO or
  reflection), the MCP servers and skills they are granted, who they may
  delegate to, and whether they may use the web.
- **The hub**: every team has a front door. It is the built-in orchestrator
  unless the team names one of its own agents, and it can plan, find an agent
  in the graph and hand it the work, search the web and read a page.
- **The graph**: each team's agents, MCP servers, tools and skills, projected
  from Postgres into FalkorDB. A run's `graph` tool searches it, scoped to what
  the caller can reach before anything is scored.
- **Skills and MCP servers**: uploaded, imported from a repository and kept in
  sync; servers discovered for their tools, with their credentials in the
  secret store.
- **Blueprints**: a team's MCP servers, skills and agents in one file, planned
  with `oa plan` and applied with `oa apply`. See [Blueprints](blueprints.md).
- **Channels**: a Telegram bot, or a `custom` signed webhook, brings messages
  in. Senders are let in by an admin or approver; a message to the team starts
  a hub run or steers the one going, and the answer goes back into the Telegram
  chat, or is posted to a custom channel's reply URL, signed. See
  [Channels](channels.md).
- **The dashboard**, served by the brain at `/ui`: a home that says what needs
  you, runs, sessions, approvals, agents, skills, MCP servers, channels, teams,
  the graph and the team's settings, and a box to ask the team something.
- Underneath: OIDC authentication, role-based authorisation where roles and
  their permissions are rows, an inbound gateway that verifies each channel's
  webhook against its own signing secret, versioned migrations, and health and
  readiness probes.

## What does not exist yet

- **Slack and Discord adapters.** The seam they plug into is written, and
  `custom` is a working generic webhook in the meantime.
- **Learnings in the graph.** It holds agents, servers, tools and skills; what a
  run learned is not projected into it.
- **A sandbox.** `code` is a pattern name the runner refuses, because running
  model-written code needs process confinement that is not written.
