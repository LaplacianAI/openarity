---
title: Schedules and watchers
---

A **schedule** runs an agent with an objective at a time, every so often, or
on a cron rule. A **watcher** runs one when a web page or a feed changes, or
when a service such as GitHub calls its address. Both run as the person who
made them, with what that person may use, and both answer where they were
told to.

Make them on the dashboard, under **Work › Schedules**, with
[`oa schedules` and `oa watchers`](../reference/cli.md), or let an
[agent with the grant](agents.md) propose one in a chat for you to confirm.

## A schedule

A schedule has a name, an objective, and a time:

- **Once**, at a day and a time;
- **every** so often, in whole minutes: every 30 minutes, every hour, every
  week, up to every 31 days;
- or on a **cron rule** of five fields, such as `0 9 * * 1-5` for weekdays at
  09:00, or `@daily`.

A once and a cron rule are read in the schedule's time zone, which is the
team's unless it names its own. The dashboard shows the next three times as a
rule is typed, in your own zone and in the schedule's.

The hub runs it and hands work on, unless it names one agent. Each run is
given the objective, whole, as its instruction.

### What a fire does

Each time is fired once. A fire can be:

| Shown as | What happened |
| --- | --- |
| Answered | It ran and said something |
| Ran, with nothing to say | It ran; nobody is told |
| Started late | The brain was down or busy at its time, and it ran soon after: within half its period, and within a day for a once |
| Caught up | Several of its times came and went while the brain was down; one run stands for them all |
| Missed | One of those times: the catch-up ran for it |
| Skipped | Its conversation was still busy with its last run, the team's runs an hour were used, its maker may not run it now, or where it answers could not be reached |
| Failed | The run failed |

Five failed runs in a row pause it. So does its maker leaving the team or
losing the right to run its agent, and where it answers, or the agent that runs
it, going away. A paused
schedule says why on its page; Resume starts it again. An admin with
`schedule:manage` can **take over** one whose maker is gone: it becomes theirs
and stays paused until they resume it.

## Where it answers

Every schedule and watcher has a conversation of its own on the dashboard,
where each run can be read. By default it answers there. It can answer instead:

- **in a chat you talked in**, on Slack, Telegram or WhatsApp, in the last 90
  days;
- **at a channel's top level**, such as a Slack channel or a Telegram or
  WhatsApp group the bot is in. This takes `channel:write`, and the app must
  be in the channel;
- **in your direct messages** with the bot. Slack opens them; on Telegram and
  WhatsApp, a chat you already have with the bot.

The picker lists every place you may choose, and shows one you cannot, with
why. Where it answers is fixed once it is made: to move it, delete it and make
another. Deleting keeps its conversation and its runs.

## A watcher

A watcher looks at one of three things.

- **A page.** Its text is compared from one look to the next: the whole page,
  or only what a CSS selector such as `#prices` matches.
- **A feed.** New entries in an RSS or Atom feed are changes.
- **A webhook.** It runs when its own address is called with a signed request.

A page or feed watcher looks as often as it is told, no more often than the
team's shortest interval. Its first look is kept to compare against. After
that, a change starts a run, which is given the objective and what changed.
A change within the **cooldown** of the last run (an hour unless set, 0 for
none, up to 7 days) is held until the cooldown ends.

A page or feed that fails to load, or loads but cannot be read, for a day
pauses the watcher. So do five failed runs in a row.

### A webhook watcher

A webhook watcher shows its address and a secret **once**, when it is made or
when **Generate secret** makes a new one. The old secret stops working at
once. Copy both straight away: the secret cannot be shown again.

**From GitHub:** in the repository, *Settings › Webhooks › Add webhook*. Paste
the address as the payload URL, choose `application/json`, and paste the
secret. GitHub signs each delivery with it.

**From anything else:** sign each request as a [custom channel's
webhook](channels.md) is signed, with a timestamp and an HMAC of the body,
keyed by the secret.

An unsigned request is refused. A delivery sent twice starts one run.

## When an agent proposes one

An agent with the **schedule** grant can propose a schedule or a watcher in a
chat, and ask to change, delete, resume or run one that runs. Nothing happens
until you confirm:

- **in the chat**, by replying with the four-digit code the agent gave you;
- **on the dashboard**, with **Confirm** on its page;
- or with `oa schedules confirm` or `oa watchers confirm`.

An ask left unanswered for an hour lapses. The team can turn proposals off
for every agent in its settings.

## What needs you

**Needs you › Scheduled** counts what your schedules and watchers did that you
have not seen: a fire that failed, was skipped or was missed; a page that
could not be read; and an answer kept in a schedule's own conversation.
Opening one marks it seen. With notifications on, the dashboard tells you of
each as it happens, naming the schedule and never what it said.

## The team's limits

**Team settings › Schedules** holds, for `team:settings`:

| Setting | Range |
| --- | --- |
| Whether agents may propose schedules and watchers | on or off |
| The team's time zone | any IANA zone |
| The shortest repeat, and the most often a watcher looks | 5 minutes to 7 days |
| Schedules and watchers per person | 1 to 100 |
| Schedules and watchers per team | 1 to 1,000, no fewer than per person |
| Runs they may start in an hour, across the team | 1 to 1,000 |

A schedule already made keeps its rule; the limits hold from its next fire or
change.

## Permissions

| Permission | What it allows |
| --- | --- |
| `schedule:read` | Seeing your own schedules and watchers, and what they did |
| `schedule:write` | Making, changing, pausing, running and deleting your own |
| `schedule:manage` | Seeing everyone's, and taking over one whose maker is gone |
| `team:settings` | The team's limits |
