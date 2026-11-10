---
title: Deleting a team
---

Deleting a team takes it away from its members at once and frees its name, but
erases nothing yet. The team, with everything it held, is kept until its purge
date and can be restored whole until then. Only a super admin can delete or
restore a team.

## Roles

A member has one role in a team. Each role can do what the one before it can:

| Role | Adds |
| ---- | ---- |
| `member` | Define agents, skills and MCP servers, and start runs |
| `approver` | See a team's channels and their queues, and let senders in or remove them (`sender:approve`) |
| `admin` | Connect and change channels, change the team's settings, add and remove members, read every conversation including direct ones |

Letting senders in is its own permission, apart from connecting channels, so an
approver can let people in without being able to rewire a channel or reach its
signing secret. An approver cannot bind a sender to an admin. See
[Channels](channels.md). Pass the role when adding someone, and see it in the
top bar and on the **Teams** page of the dashboard:

```sh
oa teams members add platform erik --role approver
```

## Deleting

From the dashboard, the team's **Team settings** page ends with a danger zone; the
name has to be typed back before the delete is sent. From the command line:

```sh
oa teams delete platform
oa teams delete platform --yes    # no prompt, for scripts
```

A delete is refused while a run in the team is queued or running: wait for it
to finish, or cancel it, and try again. Deleting a team that is already
deleted changes nothing, and keeps its first purge date.

## What a deleted team is

- **For its members** it is gone: every route in it answers as it would for
  a team that does not exist (404, or 403 where the route needs a
  permission), it is left out of their team list, and its channels refuse deliveries as if they did
  not exist.
- **For a super admin** it is read-only. It can be read, deleted again or
  restored, and every other write answers 409 with `team is deleted; restore
  it first`, so nothing changes a team that is waiting to be erased.
- **Its name is free.** A new team can take it at once.

## Restoring

Deleted teams are listed under **Team → Deleted teams** in the dashboard,
each with the date it will be erased, and with `oa teams list --deleted`:

```sh
oa teams restore platform
```

A name given to `restore` is looked up among deleted teams only, so a live
team that has taken the name since is never the one restored. Restoring then
fails with `a team with that name already exists` for as long as that team
keeps the name. Two deleted teams with the same name are reported with their ids;
pass the id of the one you mean.

A restored team's graph is the one it had when it was deleted. If every
team's graph was rebuilt while it was deleted, for a new embedding model
say, rebuild its graph too with `brain graph rebuild --team <id>`: a rebuild
of every team leaves deleted teams out.

## Purging

The worker checks once an hour and erases every team deleted longer ago than
`OPENARITY_TEAM_PURGE_AFTER`, a week by default (see
[Configuration](configuration.md)). Erasing a team removes everything it
held, and its files and secrets are removed from the object and secret stores
by the same sweep that handles every other deletion.

The purge date is computed from the setting rather than stored, so changing it
and restarting moves every pending purge at once. A team whose erasure fails
is retried on the next hour without holding back the others.
