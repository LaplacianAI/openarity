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

The grant is a field of the agent, so an update that leaves it out takes it
away, as it does `web` and `browser`: an agent file written before workspaces
existed, applied again, ends the grant. Add `workspace: true` to the file.

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
| sandboxd | The room has no route and no DNS of its own. Its HTTP and HTTPS go through sandboxd's proxy, which resolves every name and refuses a private, loopback, link-local or reserved address, and the IPv6 forms that carry one (NAT64, 6to4, Teredo). It refuses the mail ports (25, 465, 587), holds 64 connections out a room, its tunnels and plain HTTP requests together, and cuts a tunnel idle for five minutes. A program that ignores `HTTP_PROXY` reaches nothing. Raw TCP and UDP do not leave |
| E2B | The private ranges are denied, and everything else is allowed |
| Daytona cloud | Its runners are not on any network of yours |
| Self-hosted Daytona | **Refused.** It can only allow up to ten address ranges, never deny, so it cannot keep a room off the network it runs on |

The public internet includes the host's own public address: a port the host
publishes to the internet is one a room reaches, as anyone can. Keep what is
published there behind its own authentication or firewall, as sandboxd's API
is behind its token.

The room's token, which the brain sends with every call, is kept from what
runs in the room: the workspace process is made undumpable, so no command
reads its environment, and the file tools do not read `/proc`. That is
proven on sandboxd by the provider contract. On E2B and Daytona it is not
yet: their contract run is still to come. On both, the workspace's port is
also behind the provider's own credential, which the brain holds and the
room never does (E2B's traffic access token, Daytona's preview token), so a
token read inside a room is not enough to call the workspace from outside.
A provider whose commands run as root, or can `sudo`, keeps nothing in the
room from them; the workspace warns when it starts as root.

On sandboxd a workspace room has 1.5 GiB of memory, one CPU and a 512 MiB
`/workspace`, set with `SANDBOXD_WORKSPACE_MEMORY`, `SANDBOXD_WORKSPACE_CPUS`
and `SANDBOXD_WORKSPACE_BYTES`. On E2B the room template the brain builds in
the team's account has 2 vCPUs and 2 GiB; on Daytona the snapshot sets the
size.

On E2B the first room waits for that template to be built, a few minutes after
the sandbox is saved, and again after an upgrade. Until it is, a call says the
sandbox is still being prepared and nothing ran; Settings shows the build, and
why it failed if it did.

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
