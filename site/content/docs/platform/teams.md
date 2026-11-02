---
title: Deleting a team
---

Deleting a team takes it away from its members at once and frees its name, but
erases nothing yet. The team, with everything it held, is kept until its purge
date and can be restored whole until then. Only a super admin can delete or
restore a team.

## Deleting

From the dashboard, the team's **Settings** page ends with a danger zone; the
name has to be typed back before the delete is sent. From the command line:

```sh
oa teams delete platform
oa teams delete platform --yes    # no prompt, for scripts
```

A delete is refused while a run in the team is queued or running: wait for it
to finish, or cancel it, and try again. Deleting a team that is already
deleted changes nothing, and keeps its first purge date.

## What a deleted team is

- **For its members** it is gone: every route in it answers 404, it is left
  out of their team list, and its channels refuse deliveries as if they did
  not exist.
- **For a super admin** it is read-only. It can be read, deleted again or
  restored, and every other write answers 409 with `team is deleted; restore
  it first`, so nothing changes a team that is waiting to be erased.
- **Its name is free.** A new team can take it at once.

## Restoring

Deleted teams are listed under **Teams → Deleted teams** in the dashboard,
each with the date it will be erased, and with `oa teams list --deleted`:

```sh
oa teams restore platform
```

A name given to `restore` is looked up among deleted teams only, so a live
team that has taken the name since is never the one restored. Restoring then
fails with `a team with that name already exists` until that team is renamed
or deleted. Two deleted teams with the same name are reported with their ids;
pass the id of the one you mean.

## Purging

The worker checks once an hour and erases every team deleted longer ago than
`OPENARITY_TEAM_PURGE_AFTER`, a week by default (see
[Configuration](configuration.md)). Erasing a team removes everything it
held, and its files and secrets are removed from the object and secret stores
by the same sweep that handles every other deletion.

The purge date is computed from the setting rather than stored, so changing it
and restarting moves every pending purge at once. A team whose erasure fails
is retried on the next hour without holding back the others.
