---
title: The dashboard
---

The brain serves a web dashboard at `/ui` — on a local stack,
`http://127.0.0.1:21120/ui`. It signs in through the same identity provider as
`oa`, and everything in it is scoped to the team chosen at the top.

## Finding your way

The sidebar is grouped by the job a screen is for:

| Group | Screens | What they are for |
| ----- | ------- | ----------------- |
| Work | Home, Runs, Sessions, Approvals | Is the team working, what did it do, who is waiting |
| Build | Agents, Skills, MCP servers, Channels, Builder, Compose | Defining what the team can do and where it hears from |
| Understand | Graph | The team's agents, servers, tools and skills as the graph holds them |
| Team | Team settings, Teams, Deleted teams | The team's model, hub, web search and learning; its members; and, for super admins only, deleted teams and restoring one |

The team in the top bar is the one everything is scoped to, shown with your
role in it; with more than one team it is a switcher. Approvals shows how many
senders are waiting.

**⌘K** (Ctrl+K elsewhere) opens a search over every screen and, by name, the
team's agents, skills and sessions. If you may start runs, what you typed can
also be asked of the team from there.

On a narrow screen the sidebar becomes a bar at the bottom with Home, Runs and
Approvals, and a **More** menu for the rest; on a tablet it is a rail of icons.
The phone is for reading and for one-tap answers, not for defining an agent.

## The screens

**Home** says in one line what needs you: runs that failed in the last 24
hours, people waiting to be let in, servers whose last discovery failed, or
that nothing does. Under it are the last day's runs by status, each count a
link to those runs, the latest failures, the hub's recent runs, the senders
waiting and any failing servers. If you may start runs, the box at the top asks
the team a question: it goes to the [hub](hub.md), and you land on the run's
trace to watch it. Ctrl+Enter (⌘+Enter on a Mac) sends it.

**Runs** lists every top-level run, newest first, filtered by status, by whether
the hub or a named agent answered, and by time. The filters are in the address,
so a filtered view can be linked. A run handed to another agent is part of the
run that started it, and its trace shows who it was delegated to, and a
delegate's run says who delegated it.

**Sessions** lists the team's conversations, with the channel each came from,
who is in it, its last message and its latest run, and filters by channel and
by the status of a run. A session is named by the provider's reference when it
has one, then by what was first asked in it, then "Direct session". Opening one
shows the **Conversation** and its **Runs**, each with its trace: tool calls,
delegations and fetched pages. Under a channel's answer is a line saying
whether the reply was sent, when, or why not; see [Channels](channels.md).

**Approvals** is the queue of people who wrote in on a channel and have not been
let in, the longest waiting first, with a **Let in** action that binds them to
a member (it suggests the member whose name fits, when exactly one does). A
second tab lists who is already let in. See [Channels](channels.md).

**Agents** is a table of the team's agents with their model, grants, whether
they may use the web, and whom they hand work to. **Define agent** and each
agent's own page are for creating and editing one: its instructions, pattern,
model, grants and delegation. Saving replaces the whole agent. The page also
deletes it.

**Skills**, **MCP servers** and **Channels** are tables too, and a row opens a
sheet. A skill's sheet shows its description, source and files, and which agents
hold it; adding one uploads a folder or zip, or imports from a repository. A
server's sheet shows its tools, when discovery was last tried and why it failed,
and has **Discover** to ask it again; its credentials are references into the
secret store, never the secret. A channel's sheet shows its webhook address and
**Replies to**, and lets you set where replies go or stop them.

**Builder** is a conversation with the team's builder: say what agents, MCP
servers or skills you want, answer what it asks, and apply what it proposes.
Each proposal is a card with its plan as it would apply for you; a refusal,
including a permission you lack, is shown and Apply is not offered. See
[Blueprints](blueprints.md#the-builder).

**Compose** gathers new and changed MCP servers, skills and agents into one
draft, through the same forms the other screens use, and writes nothing until
**Apply**. An agent may be granted a server or skill the draft creates, and
**Change an existing one** opens one of the team's own agents or servers.
**Plan** shows what applying the draft would change, with every refusal; Apply
appears only for a plan with changes and no refusals, applies exactly that
plan, and any change to the draft takes the plan away. It is a
[blueprint](blueprints.md), applied in one transaction.

**Teams** lists the teams you can see and their members, with each member's
role: `member`, `approver` or `admin`. **Team settings** has four groups: the
model, the hub (and when a transcript is compacted), web search, and learning.

**Graph** draws the hub, the agents and whom they hand work to, and the servers,
tools and skills they are granted; it can be shown as a list. A search box
finds a node by name. Changes show within about a minute.

Everything is in the permissions of the signed-in person. A write you may not
make is shown with the permission it needs, rather than hidden, and the brain
still checks every request.

Sign-in needs a secure context, so a dashboard reached at a LAN or tailnet
address must be served over HTTPS; on the machine itself, `127.0.0.1` works
over plain HTTP.

## Not built

- **Rejecting a sender.** Someone never let in never reaches an agent, but
  there is no button that refuses them.
