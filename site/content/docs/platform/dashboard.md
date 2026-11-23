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
| Build | Agents, Skills, MCP servers, Channels, Builder, Blueprint, Improvements | Defining what the team can do and where it hears from, and the changes its agents propose to themselves |
| Understand | Graph | The team's agents, servers, tools and skills as the graph holds them |
| Team | Team settings, Teams, Deleted teams | The team's model, hub, web search and self-improvement; its members; and, for super admins only, deleted teams and restoring one |

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

**Improvements** lists the changes the team's agents propose to their own
harness: instructions, description, skills and their files, servers and tools,
limits, delegation and loop. Each shows the agent, why it proposed the change,
what it expects to happen, the runs it came from (linked when you may read
them), and the change itself as a line diff, names added and removed, or a
value before and after; a skill's script is shown in full. Marks say when a
change widens what the agent can reach or raises its cost, and a skill edit
names every agent that holds the skill. An approver sees **Waiting**,
**Applied** and **Closed** (rejected, reverted and withdrawn) and decides with
**Approve**, **Reject** or **Revert**; reject and revert ask once more.
Everyone else sees what was applied. A change is refused if its agent or skill
has changed since it was proposed, and approving one withdraws the others
waiting on the same target.

The changes come from the **improver**. When one signature (the part of an
agent's setup involved and the cause) recurs in three of its runs, or once a
day for an agent with enough judged runs since its last pass (five until the
team changes it) and something new to read, a pass reads the agent's setup, the digests
behind the pattern, up to ten of its runs in full and its record of earlier
proposals, and proposes zero or more changes; proposing nothing is the usual
answer. A daily pass has no pattern of its own, so it may answer any failure it
was shown. It also offers to drop a skill the agent held in each of its last 50
runs and never opened, or a tool it never called; a candidate it looked at is
not offered again for 30 days, and the Passes tab marks each one kept or
proposed for removal. Each proposal is held to its
kind's rules and screened by the judge model for one conversation's details (a
name, an id, a quoted message) before it is stored. A newer pass's proposal replaces one an earlier pass left
waiting on the same pattern, with the same kind of change to the same target; a
person's own proposal, and one of another kind, wait on. Anything a pass read
that someone outside the team spoke in bars it from proposing a change that
widens the agent. An approver's **Passes** tab lists every pass: why it ran,
what came of it, its tokens, and its report, including why it left a pattern
alone; a report drawn on a conversation the approver cannot read is withheld.
A pass runs on the judge model, else the team's. Team settings cap what it
costs: the passes a day (20 until changed), the tokens a pass may spend across
its tries (400,000), the minutes for each try (20), and the judged runs an
agent needs since its last pass before the daily pass looks at it (5); past
the tokens or the minutes it stops
and reports what it found. A pass cut short this way (or by improvement being
turned off) with nothing proposed leaves its pattern open, offered again after
six hours, and the Passes tab says it was cut short.

Every applied change is watched. The agent's next ten judged runs are counted
against the ones just before the change, measured against how much its runs
usually vary: from up to 50 runs before it, the brain works out how many bad
runs out of ten chance alone would give less than one time in twenty, and a
change whose runs reach that is **flagged** as worse. A change whose runs cost
clearly more (their median tokens above the usual 90th percentile) without
failing less is flagged as costlier. With fewer than ten runs before it, a
change is watched but cannot be flagged. The card says how it went in words,
with the tokens and steps a run, whether the pattern it targeted became rarer,
👍 and 👎 before and after, and other changes applied to the same agents
meanwhile. Flagged changes collect on the **Flagged** tab, where someone who
may approve clears the flag (keeping the change) or reverts it; nothing is
reverted by itself. Runs only count while improvement is on, and a change that
gets too few runs in 30 days stops being watched.

A team may also turn on **Also make good runs cheaper and faster**, off until
set. Once a week, on Sunday at 04:00 UTC, an **efficiency pass** looks at each
agent with enough runs judged good in a window (20 in 14 days until changed),
whose costliest good runs cost at least a multiple of its usual one in tokens
or work time (2×), or whose runs show one sign of waste in enough of them (3):
the same call made twice, a failed call made again, 20 or more tools offered
and 2 or fewer used, a run using 80% of its steps, or a tool that took over 30
seconds. Work time leaves out time spent waiting for a person's approval. An
agent with a pattern still open, a change waiting, or a change still watched or
flagged is left alone. The pass reads the costliest good runs beside cheap ones
on requests like them, and may only propose what makes a run do less: an
instruction, a tool note, a narrower tool list, a dropped skill, a lower limit,
a narrower delegation, a move from the Plan or Reflection loop to the plain
ReAct loop, or a skill edit without a script. It never proposes a different model. An efficiency pass counts against
the team's passes a day; one past the cap waits for the next week. Its proposals are marked **Saves cost** or **Saves
time**. Once applied, such a change is flagged as worse a number of bad runs
sooner than any other (one until changed), and flagged **no saving** when the
median tokens or work time of its good runs, whichever it aimed at, did not
fall, both of them for a change aimed at both; its card shows both. Each of
these numbers is a team setting, explained beside its field, and a change to the
margin applies to changes still being watched.

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
including a permission you lack, is shown and Apply is not offered. Opened
with no conversation, it lists your **Recent conversations**, so one is found
again after you leave it, and a builder run's trace links back with
**Continue in Builder**. See [Blueprints](blueprints.md#the-builder).

**Blueprint** (once called Compose; old links still land there) lists what the
builder proposed that nobody has applied yet, each opening its conversation to
review and apply, then gathers new and changed MCP servers, skills and agents into one
draft, through the same forms the other screens use, and writes nothing until
**Apply**. An agent may be granted a server or skill the draft creates, and
**Change an existing one** opens one of the team's own agents or servers.
**Plan** shows what applying the draft would change, with every refusal; Apply
appears only for a plan with changes and no refusals, applies exactly that
plan, and any change to the draft takes the plan away. It is a
[blueprint](blueprints.md), applied in one transaction.

**Teams** lists the teams you can see and their members, with each member's
role: `member`, `approver` or `admin`. **Team settings** groups the model,
the hub, compaction, web search, the sandbox, the browser, and whether the
team's agents improve themselves from their runs. While that is on, every
finished run is judged in the background on whether the task was done, by the
team's model or a judge model chosen here, and a share of the good runs (10%
until changed) is studied beside every bad one, as is a run that ended well
after one of its tools failed twice or more on the way. Under each answer in a
conversation, a member who may run agents can press 👍 or 👎, with a line on
what was wrong; that outranks the judge for that run. A run's trace shows how
it was judged, and how many members gave 👍 and 👎, never who. Judging
sends the run's conversation, trace and tool results to the same model
endpoint the team's agents use, and what it says is stored with the run.
While that endpoint cannot be reached, a run is tried again every hour for
a day before it is marked as one the judge could not tell.
Only runs that finish while improvement is on are judged: turning it off
and on again starts from that moment.
To see it working without opening the dashboard, read the worker's log. Each
judged run logs `A run was judged` with its verdict, and each digested run
logs `A run was digested` (or `A run's digest was skipped` when the model
could not study it). Each pass logs `An improver pass began` and then
`An improver pass finished`, with how many changes it proposed and how many
the screen held back; a failed pass also logs a warning with its cause. These lines carry ids and counts, never what a run or a
report said.
Compaction says when a run folds its earlier turns into a summary: past a
number of messages, and when its next request would read a tenth of its
token budget, held between an at least and an at most the team chooses
(50,000 and 60,000 until it does, each from 30,000 to 1,000,000). The
section says in a sentence what the two lines do, refuses an at least above
the at most before saving, and offers the defaults back. A run's transcript shows each
compaction, with the summary that replaced the older turns, and a
**Wrapping up** line when it was asked to finish before a limit. A run that
stopped at its budget shows what it had under **What it had when it stopped**.

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
