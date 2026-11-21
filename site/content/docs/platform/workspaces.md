---
title: Workspaces
---

An agent granted a workspace can run commands and work with files: it has
six tools, `Bash`, `Read`, `Write`, `Edit`, `Glob` and `Grep`, named as
Claude Code names them, in a sandbox room that belongs to the conversation.

```yaml
# coder.yaml
name: coder
description: Writes and runs small programs to answer questions about data
instructions: Write the script to a file, run it, and say what it printed.
model: {name: anthropic/claude-sonnet-5-5, max_tokens: 200000}
workspace: true    # a sandbox of the conversation's own; off unless set
```

The grant needs the team's sandbox, the same one its
[command MCP servers](mcp-servers.md) run in:

```sh
oa teams settings set platform --sandbox e2b --sandbox-key-stdin
```

A team with no sandbox can still grant it. Until one is chosen, each run's
trace shows the six tools as unavailable and says why, and the dashboard says
so beside the switch.

## One room per conversation

The room is made by the first call that needs it and is shared by every agent
working on the conversation, delegates included, so a file one writes is there
for the next. It stays while it is used, and an idle room is deleted after
`OPENARITY_SANDBOX_IDLE` (thirty minutes by default) with what was in it. A
conversation deleted takes its room with it. The [hub](hub.md) never has a
workspace: it hands the work to an agent that does.

Each `Bash` call starts in `/workspace` in a fresh shell, so `cd` and exported
variables do not carry over, and anything the command left running in the
background is stopped when it ends. A command may run for up to 270 seconds,
two minutes unless it asks for more. Its output is cut to its first and last
128 KB, and `Read` returns 2,000 lines at a time.

The room image has `python3`, `node`, `npm`, `uv`, `git` and `curl`. A script
in any of them is a file the agent writes and runs with `Bash`.

## What it may reach

The public internet, and nothing private: not the brain, not Postgres or
OpenBao, not the cloud's metadata address, not anything on the host's own
network. Each provider is held to that by the same test, through the provider
interface alone, so one added later is held to the same:

| Provider | How |
| --- | --- |
| sandboxd | The room has no route and no DNS of its own. Its HTTP and HTTPS go through sandboxd's proxy, which resolves every name and refuses a private, loopback or link-local address. A program that ignores `HTTP_PROXY` reaches nothing. Raw TCP and UDP do not leave |
| E2B | The private ranges are denied, and everything else is allowed |
| Daytona cloud | Its runners are not on any network of yours |
| Self-hosted Daytona | **Refused.** It can only allow up to ten address ranges, never deny, so it cannot keep a room off the network it runs on |

On sandboxd a workspace room has 1.5 GiB of memory, one CPU and a 512 MiB
`/workspace`, set with `SANDBOXD_WORKSPACE_MEMORY`, `SANDBOXD_WORKSPACE_CPUS`
and `SANDBOXD_WORKSPACE_BYTES`. On E2B and Daytona the template or snapshot
sets the size.

## Approvals

Every call passes the team's approval policy.
`Bash` is a write: a command can send anything to any public host. `Read`,
`Write`, `Edit`, `Glob` and `Grep` read or change only the conversation's own
room, which nothing outside it sees, so they are reads. With the team's mode at
`writes`, every command asks a person; a rule `allow Bash` turns that off. A run
nobody is present for is refused a command that would ask.

An agent cannot propose a workspace for itself as an improvement; a person
grants it.

## Not built

- **Files outlive the room** only while it is in use. Saving `/workspace` when
  an idle room is deleted, and bringing it back, comes next.
- **A granted skill's scripts** are not yet copied into the room.
- **A file in the room cannot yet be sent** to the person or read as an
  attachment.
