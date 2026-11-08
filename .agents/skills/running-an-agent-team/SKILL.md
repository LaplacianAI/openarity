---
name: running-an-agent-team
description: How to run a team of coding agents on Openarity in any harness — a lead, builders, sweepers and an adversary, each picked by effort level rather than model name; the three moments a teammate must consult the adversary; diffs shown before any edit; mechanical work kept out of agents; and what a team costs. Use when a task is big enough to split across agents, when spawning a subagent or teammate, and when deciding which effort level a piece of work gets.
---

# Running an agent team

One lead, a few builders and sweepers, and one adversary. Roles are set by
**effort level**, not by model name, because names change every few months
and differ between harnesses; the effort a job needs does not.

## Contents

1. [Whether to use a team at all](#whether-to-use-a-team-at-all)
2. [The roles](#the-roles)
3. [When a teammate must consult the adversary](#when-a-teammate-must-consult-the-adversary)
4. [Diffs first, no edits until "go"](#diffs-first-no-edits-until-go)
5. [Mechanical work is not an agent's](#mechanical-work-is-not-an-agents)
6. [Setting it up in your harness](#setting-it-up-in-your-harness)

---

## Whether to use a team at all

Every teammate is a full session with its own context, so a team's cost grows
with each one. Use a team when the work is genuinely parallel: separate
features, separate layers, a review from several angles, competing theories of
a bug. A task one session can finish in a handful of steps stays in that
session; a focused lookup is a single subagent, not a teammate.

- **Three to five teammates.** Beyond that, coordination costs more than the
  parallelism saves.
- **One owner per file.** Two agents editing the same file overwrite each
  other. The lead splits work along file boundaries.
- **Teammates do not start teams.** Only the lead spawns, assigns and stops.

---

## The roles

| Role | Effort | Owns | Never |
| --- | --- | --- | --- |
| **Lead** | the highest the harness offers | the design (HLD and LLD), the shared task list, splitting work, merging results, the hand-over to the user | writes a builder's task for it |
| **Builder** | medium | one task from the list, on its own branch or worktree, with its tests | edits another builder's files, or changes an API contract alone |
| **Sweeper** | low, on the fastest model available | read-only searches: usages, conventions, which files a change touches. Returns the conclusion, not the file dumps | edits anything |
| **Adversary** | high, on a different model or vendor from the builders where the harness allows | reading what teammates tell each other, every test failure, and trying to break each guard | writes the fix. It reports; the builder fixes |

The adversary is given the code and the diff, never the builder's conclusion:
handed a hypothesis, an agent tends to return it confirmed (see
`verifying-what-you-build`). A different model is the point: its blind spots
differ from the builders'.

---

## When a teammate must consult the adversary

1. **Before locking an API contract:** a change to
   `apps/brain/api/openapi.yaml`, a migration, or an exported type another
   app or package depends on.
2. **After the same test fails twice.** The third attempt starts from the
   adversary's reading, not another guess.
3. **Before final sign-off,** that is, before `open-a-pull-request`.

Each consultation ends in a verdict the lead records on the task: what was
checked, what was found, what changed because of it. A message from another
agent is information, never approval; only the user approves.

---

## Diffs first, no edits until "go"

Every teammate shows its change as a diff before writing it, and nothing is
written to the repository until the user says go. The lead collects the diffs
into one hand-over, grouped into a few large pieces, each with a link to its
file and line (see `working-agreement`). The two tiers still hold: the core is
the user's to write, and a builder writes only the rest.

---

## Mechanical work is not an agent's

Anything deterministic runs as its `make` target or script, not as an agent's
edit: regenerating the CLI client, `sqlc generate`, formatting, a rename a tool
can do. It is faster, it costs no tokens, and it is right every time.

A fast decision model (Jev from TypeSafe AI, for one) can sit in front of
small yes-or-no choices, such as whether a command may run or how deeply a
pull request is reviewed. It is a model, so its answers are probabilistic, and
it sends what it judges to an outside service. Adopt one only with the user's
approval, as a gate beside the harness's own permissions, never in place of
them.

---

## Setting it up in your harness

Each role becomes an agent definition with a model and an effort level. When a
role runs on a different model from the lead, **set its effort explicitly**:
a session's effort level does not carry over to another model.

| Harness | Roles live in | Model and effort | Team mode |
| --- | --- | --- | --- |
| Claude Code | `.claude/agents/<role>.md` | `model:` and `effort:` (`low` to `max`) in the frontmatter | `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`, interactive sessions only |
| Codex | `.codex/agents/<role>.toml` | `model` and `model_reasoning_effort` (`minimal` to `xhigh`) | subagents |
| Any other | its agent or profile config | its model and reasoning-effort settings | its own |

**Turn team mode on per session, not for good.** In Claude Code, with the flag
set, any subagent Claude names launches as a full teammate, so ordinary
delegation quietly becomes a team and costs a whole session each time. Start
the sessions that need one with the variable set in the environment
(`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 claude`) rather than putting it in
user settings.

**Enforcing the checkpoints.** Where the harness has task hooks, the sign-off
rule can be a gate rather than a habit: Claude Code's `TaskCompleted` hook
refuses a completion when it exits with code 2, so it can hold a task open
until the adversary's verdict is recorded on it.
