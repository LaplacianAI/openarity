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
for the next. A conversation deleted takes its room with it.

The room stops when the conversation's last run ends, and holds no memory
while stopped. Its files stay: the next run that calls it starts it again
with `/workspace` as it was left. A room left running, such as one whose run
is waiting for a person's approval, stops after the team's idle minutes, or
`OPENARITY_SANDBOX_IDLE` (thirty minutes by default) when the team has set
none. A stopped room is deleted with its files after the team's keep hours,
24 unless the team sets otherwise, from 1 to 720, or sooner on sandboxd when
its disk budget is spent (see [Size](#size)):

```sh
oa teams settings set platform --keep-stopped-hours 72 --stop-idle-minutes 15
```

The same settings are under Workspaces in the Sandbox section of the
dashboard's Settings. A change to the keep hours counts for rooms already
stopped too.

A room that cannot be brought back as it was (the provider lost it, it does
not answer after a resume, or it is over its disk) is replaced by a fresh
one. The call that found it does not run: the agent is told its earlier
files are gone, and its next call uses the fresh room. Calls made at the same
moment by other agents of the conversation reach the fresh room without the
notice. The old room is deleted before the new one is made, since a sandbox
with room for only one would refuse the new one otherwise; if the new one is
refused too, the conversation has none until a later call. A [hub](hub.md) the team chose
from its agents has one when it is granted one, like any agent, and can work
in it as well as hand work on; the built-in hub has none.

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

## Size

A team may give its workspaces a size of its own, memory, CPUs and disk
together, so a team that trains models can have 8 GB while another has 1:

```sh
oa teams settings set ml --workspace-memory-mb 8192 --workspace-cpus 4 --workspace-disk-mb 20480
oa teams settings set ml --no-workspace-size     # back to the provider's
```

A change gives each conversation a new room on its next call, without the
files of the old one. With none set, the provider's size is used:

| Provider | Without a size | With one |
| --- | --- | --- |
| sandboxd | 1.5 GiB of memory, one CPU and 512 MiB of disk | Up to what its operator gives a workspace, `SANDBOXD_WORKSPACE_MEMORY`, `SANDBOXD_WORKSPACE_CPUS` and `SANDBOXD_WORKSPACE_BYTES`; past that the room is refused |
| Daytona | Its snapshot's | Memory and disk rounded up to whole GB |
| E2B | Its template's, 2 CPUs and 2 GB | A template built at it in the team's account; new workspaces wait a few minutes for it, and a size the team's E2B plan refuses shows as a failed build in Settings. Disk is the free space E2B makes room for, best effort |

sandboxd's disk is a soft cap: it measures every 30 seconds and stops a room
past its size. Every room's disk together, stopped rooms' too, is held to
`SANDBOXD_DISK_BUDGET` (8 GiB by default). A new room is let in while what the
rooms hold now, plus its own disk (512 MiB unless the team set a size), fits:
at the defaults, about sixteen workspaces that filled their disk. When it does
not fit, sandboxd deletes the stopped workspace stopped longest, then the
next, until it does; running rooms are never touched. If the stopped rooms
cannot free enough, the new room is still refused as busy. The next call of a
deleted workspace's conversation gets a new room, and the agent is told its
earlier files are gone. sandboxd does not know teams, so the workspace it
deletes may be another team's. A team that needs its files kept should keep
the budget large, or its state elsewhere.

Upgrading sandboxd: a workspace room made before stopping existed has no
volume for `/workspace`, so its files would not survive a stop. Delete such
rooms when upgrading; the next call makes each conversation a new one:

```sh
docker ps -aq --filter name=oa-sb- | xargs -r docker rm -f
```

Changing a team's sandbox provider, its address or its key does the same:
rooms made with the old ones are deleted, and each conversation gets a new
workspace, without its files, on its next call.

Upgrading the brain: a room is made from the image of the brain's release,
so a deploy of a new commit gives each conversation a new workspace on its
next call, without the old one's files, and the agent is told so. Keep
anything that must outlive a release somewhere else: a repository it
pushes to, or an attachment.

On E2B the first room waits for that template to be built, a few minutes after
the sandbox is saved or its size changes. Until it is, the run waits for it, as
below; Settings shows the build, and why it failed if it did.
After an upgrade, rooms start from the last template built at the same size
while the new one builds, so nothing waits. A team that names its own E2B
template gets that template's size, whatever size it sets.

## When the sandbox has no room

A call the sandbox refuses before anything runs waits for room instead of
failing. Three refusals wait:

- **Full:** every share of sandboxd's memory or disk budget is in use, or
  E2B's limit is reached.
- **Being prepared:** E2B's template for the team is still being built.
- **Not answering:** the provider could not be reached, timed out, or a
  gateway in front of it said it was down, as during its own deploy. A
  certificate it refuses, an address it cannot use or a host name that does
  not exist is a setting to fix, and stays the call's error.

The call is not made. The agent says in a line that it is waiting, the person
on a channel is sent that line, and the run parks holding no worker, as it
does for an approval. Every minute the brain looks for waits whose try is due
and makes the room itself, so a try costs the agent nothing: a minute after
the refusal, then after as long as the run has waited, about two minutes apart
at most. Once the room is made the run carries on, is told there is room, and
makes the call again. At the first try after an hour (so a couple of minutes
past it) it is told the sandbox still
had no room and goes on without it. A person who
writes in the conversation meanwhile ends the wait at once, and the agent reads
what they wrote.

A delegate waits the same way, and its parent with it: the run that asked is
told its delegate waits for the sandbox, not for a person.

Not waited for, because waiting does not change them: a command that ran out
of its room's memory or disk (exit code 137, `No space left on device`), an
image sandboxd cannot pull, a provider that refuses the room's size, and a
room lost in the middle of a call, which may have run. Those are the call's
error, as before.

## A file the agent made

A file in the room, such as a chart a script drew or a report it wrote, is
handed to the person in two steps. `keep_file(path)` reads it out of the room
and keeps it as a file of the conversation, which `read_attachment` reads and
`send_file` sends:

```text
Bash         python3 plot.py            → wrote out/chart.png
keep_file    {"path": "out/chart.png"}  → Kept chart.png (image/png, 48 KB) as file 7c2e…
send_file    {"id": "7c2e…"}            → the person gets chart.png
```

A path is from `/workspace` unless it is absolute; any regular file in the
room can be kept, up to 100 MiB, apart from those under `/proc`. A run tries
twenty at most, a try that fails among them. A run that holds `keep_file` holds
`read_attachment` with it, and `send_file` wherever it can send, so a file
kept is read or sent in the same run. A file kept stays with the conversation however long the room
does. A room replaced since the file was written no longer has it, and the
agent is told so.

## Approvals

Every call passes the team's approval policy.
`Bash` is a write: a command can send anything to any public host. `Read`,
`Write`, `Edit`, `Glob`, `Grep` and `keep_file` read or change only the
conversation's own room, which nothing outside it sees, so they are reads. A
run that also holds a command MCP server is judged on `keep_file` as on
`send_file`, since that server's room is shared by the team's conversations:
see [MCP servers](mcp-servers.md). With the team's mode at
`writes`, every command asks a person; a rule `allow Bash` turns that off. A run
nobody is present for is refused a command that would ask.

An agent cannot propose a workspace for itself as an improvement; a person
grants it.

## Not built

- **Files outlive the room** only until its keep hours end, or sooner on
  sandboxd when its disk budget is spent. Saving
  `/workspace` somewhere longer-lived comes later.
- **A granted skill's scripts** are not yet copied into the room.
- **A file over 100 MiB** cannot be kept.
- **A file over a channel's limit goes to it** only as a mention (Slack and
  WhatsApp 100 MiB, Telegram 50 MB, custom 25 MiB; see
  [channels](channels.md)): it can be downloaded from the conversation on the
  dashboard, and `send_file` says so.
