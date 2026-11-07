# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

**Today: one person in every role.** A small team or a solo self-hoster runs
Openarity, and the same person is its admin, its agent builder and its
operator. They decide who is in a team, define the agents and what each is
granted, and watch what those agents do.

**Tomorrow: the roles split.** The dashboard is designed for that one person
but must still make sense when the roles are different people: a member who
only reads, an operator who never edits an agent, an admin who never reads a
trace. Permissions already decide what each of them may do (`agent:write`,
`membership:write`, `run:write`, `super_admin` and the rest); a screen shows a reader what is there
and says plainly what they cannot change, rather than hiding it.

**Where and how often.** Real work happens on a desktop. Quick checks happen
on a phone: did that run fail, is someone waiting to be let in, is a server
down. The phone is for reading and for one-tap answers, not for defining an
agent.

## Product Purpose

Openarity is an open-source agent platform where agents, tools, skills and
learnings are a knowledge graph rather than a list: a run picks what it uses
by walking relationships, and a hub agent delegates through the team's graph.

The dashboard is where a team is run. **Anyone can run a team from it
entirely**; the `oa` CLI covers the same ground for scripts and for people who
prefer a terminal, and is optional.

Success is that the one person can answer, without leaving the dashboard:
*is my team working, what did the agents do, and what needs me?* And then act
on the answer in a click or two.

## Positioning

What a neighbouring agent console could not truthfully copy:

- **The graph is the system.** Agents, MCP servers, skills and learnings are
  nodes with edges of authority and provenance. What a run *may* use and what
  it *can* reach is the same traversal, and the dashboard can show it.
- **A hub that delegates through that graph.** A run on a team can go to the
  hub, which hands work to the agents it can reach. Which delegate answered,
  and why, is part of what a run is.
- **The harness around a run is structural.** Identity, roles that are rows
  rather than constants, spend counted where the calls happen, and team
  deletion that destroys the team's key before its bytes.
- **Every channel is the same message.** Slack, Discord, Telegram and webhooks
  normalise into one format, so a session reads the same wherever it came
  from.

## Operating Context

The dashboard is served by the brain at `/ui/`, behind the brain's own
sign-in (OIDC, PKCE). A person picks a current team; every screen but Teams
and Deleted teams is about that team.

The four jobs the one person comes to do, **equally often** — none ranks
above the others, so Home serves all four:

1. **See what the agents did.** Recent runs: succeeded or failed, why, where
   the tokens went, which delegate answered. Read a trace; cancel a run that
   is still going.
2. **Change an agent.** Its instructions, model, pattern, and its grants of
   MCP servers and skills; then try it.
3. **Ask the team something.** Start a run and watch it work. The question
   goes to the hub (`POST /runs` with no agent), from the box on Home or from
   ⌘K, and the asker lands on the run's live trace.
4. **Let people in.** Approve senders waiting on a channel and bind each to a
   user, add members to a team, connect a channel.

Live things are part of the scene: a run streams its events while it works, a
session gains messages, a sender appears in the queue.

## Capabilities and Constraints

What the dashboard does today, and must keep doing through any redesign:

- **Teams:** list, create, add and remove members, delete; a super admin
  restores or waits out a deleted team from Deleted teams.
- **Channels:** connect (custom webhook and others), delete; approve or
  remove senders.
- **Sessions:** list, read the conversation (messages and each run's answer)
  and attachments, read the session's runs,
  follow a run's event stream, cancel a run.
- **Agents:** define, replace (a whole PUT), delete; grant MCP servers and
  skills from the same team.
- **MCP servers:** register, replace, delete, run discovery, list tools.
- **Skills:** upload, import from GitHub, sync, replace, delete, read the body.
- **Graph:** read the team's graph.
- **Team settings:** the built-in hub's model and settings.

Constraints:

- Stack is fixed: React 19, TanStack Router and Query, Tailwind v4, Radix,
  Vite. A new dependency is asked about first.
- The token lives in session storage, never in a URL or a log.
- User-facing text never names a backend setting, environment variable or
  code identifier; it says what happens in plain words.
- Monospace is for code and ids only (raw JSON, tool arguments, copyable
  ids). Names, emails, counts and paths are text.
- Targets are light and dark, at 1440, 768 and 390 pixels, all production
  grade.

Undecided, for later gates:

- Whether runs get a screen of their own apart from sessions.
- Whether the agent editor becomes a page rather than a dialog.

## Brand Commitments

From [BRAND.md](../../BRAND.md), which is the source:

- The name is **openarity**, lowercase, beside the mark.
- The mark is the squircle O cut on the diagonal; its palette and rules are in
  BRAND.md and are copied from there, never changed here first.
- Voice is plain and exact. It says what happens and why, in full sentences,
  without marketing: "There is no 'reject'. A sender that is never approved
  never reaches an agent, so declining is what happens by leaving them here."

## Evidence on Hand

- A seeded **showcase** team on staging: 7 agents (one the hub), 3 MCP servers
  (one whose discovery fails), 2 skills, 2 channels with 21 sessions, senders
  waiting, and runs that succeeded, failed, were cancelled, delegated and are
  still running. Seeded by `scripts/seed-showcase.sh`.
- The "before" captures of every screen, both themes, three widths, in
  `.captures/before/` (local, not committed), taken by `make capture`.
- There are no customers, testimonials, usage numbers or benchmarks for the
  dashboard. None may be invented.

## Product Principles

1. **Answer "does anything need me?" first.** Failures, waiting senders and
   broken servers come before inventories.
2. **Everything is doable here.** A job the brain allows and the dashboard
   cannot do is a gap, not a CLI feature.
3. **Show the graph's reasons.** Where the product's mechanism explains an
   outcome (which delegate, which grant, what is unreachable), say it.
4. **Read on a phone, build on a desk.** Every screen is readable at 390 and
   answers its question there; editing may assume a desk.
5. **Say what a reader cannot do, and why.** Permissions hide nothing a person
   is allowed to see.

## Accessibility & Inclusion

WCAG 2.2 AA, checked at every gate: contrast in both themes, full keyboard
use with a visible focus, accessible names on every control, reduced motion
honoured, and targets large enough to tap at 390.

## Screens

One question per screen, the finding it surfaces first, and the job one click
away. Magnitudes are working estimates for a small team, to design against,
not measurements.

| Screen | Magnitude | One question | The finding it surfaces | Most frequent job, one click away |
| --- | --- | --- | --- | --- |
| Home (now Overview) | — | Is the team working, and does anything need me? | Runs that failed in the last 24 h; senders waiting; a server whose discovery failed | Open the failed run; ask the team |
| Sessions | tens to thousands | Which conversations are live or need attention? | Sessions with a running or failed run | Open a session |
| Session detail and run trace | 1–50 runs, traces of 1–100 steps | What did the agent do, and did it work? | Where the run failed or spent its tokens; which delegate answered | Read the failing step; cancel a running run |
| Approvals | 0–20 waiting | Who is waiting to be let in? | The oldest waiting sender, and how long they have waited | Bind the sender to a user |
| Agents | 3–30 | Which agents exist, and what can each do? | Agents with no grants, or none reachable through the graph | Edit an agent |
| Agent editor | one agent | Is this agent set up to do its job? | A grant missing for what the instructions ask for (later) | Save |
| MCP servers | 1–20 | Which tool servers are connected, and do they work? | A server whose discovery failed, and when | Re-run discovery |
| Skills | 1–50 | What can agents load, and from where? | A skill no agent is granted | Import a skill |
| Channels | 1–10 | Where do messages come from, and are they arriving? | A channel silent longer than usual | Open its sessions |
| Graph | tens to hundreds of nodes | How is the team wired? | Something in the team unreachable from the hub | Search the graph |
| Team settings | one team | How does this team's built-in hub run? | No model set, so the hub cannot run | Set the model |
| Teams | 1–10 teams, 1–20 members each | Who is in which team? | A team with no admin | Add a member |
| Deleted teams | 0–5 | Which deleted teams can still come back, and until when? | The team closest to being purged | Restore a team |
| Sign in | — | Get in | (none) | Sign in |
