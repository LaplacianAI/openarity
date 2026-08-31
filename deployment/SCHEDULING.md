# Running the reaper on a schedule

Deleting a team, a channel or a person removes their rows. It cannot remove the
attachment bytes in the object store or the secrets in the vault, because
Postgres has no transaction that spans them. Each deletion records what it owes
instead — a tombstone written by a trigger, in the same transaction that
removed the row — and `brain reap` completes it.

A deployment that never runs it never erases anything outside Postgres. This
file is how to make sure something does.

## Two ways, and the default is the first

| | What runs | Owns the clock |
| --- | --- | --- |
| `brain worker` | a long-lived process, in every compose target and the k8s manifests | itself |
| `brain reap` | one sweep, then exits | your `cron`, systemd timer, or a Kubernetes `CronJob` |

`brain worker` is what the deployment files here run. It sweeps every fifteen
minutes, and **replays the ticks it missed while it was down** rather than
skipping them — the one thing a `CronJob` cannot do, because
`startingDeadlineSeconds` can only skip.

`brain reap` stays a standalone command that needs none of that. Erasure is a
legal obligation, so it should not be reachable only through the durable
runtime: if the worker is misconfigured or the library disappoints, the command
still works, by hand or from cron. Both run the same `reaper.SweepAll`, so the
fallback cannot drift from the thing it backs up.

```sh
docker compose -f docker-compose.yml run --rm --no-deps brain reap
```

`--no-deps` is not optional on a schedule: the `brain` service depends on
`migrate` completing, so without it every sweep re-runs migrations.

## What the worker actually does

It hosts **jobs**. A job registers its workflows before the durable runtime
launches — DBOS builds its registries in memory and reads them at `Launch` —
and returns the schedules it wants installed after, because installing one
needs a running scheduler. The reaper is the first job; agent runs will be the
second.

Three behaviours worth knowing before operating it:

- **It sweeps once at startup.** That proves every dependency the reaper needs
  — the secret store can delete, the object store exists — before the process
  calls itself healthy. It also means a fresh deployment does not sit idle for
  a full interval, since a schedule installed for the first time has no missed
  ticks to backfill.
- **An overdue erasure does not stop it starting.** Refusing would deadlock the
  only thing that can clear the backlog: the alarm would block its own cure. It
  logs at `ERROR` naming the oldest outstanding item and carries on.
- **Two replicas are safe and unnecessary.** The sweep claims with `FOR UPDATE
  SKIP LOCKED`, so a second divides the backlog rather than duplicating it, and
  no leader election is involved. There is nothing here to scale for.

## The interval

Fifteen minutes, as a constant in `internal/reaper/job.go` rather than
configuration — the same treatment `MaxAttachment` gets, with a test that
parses it. It is sized against the twenty-four hours at which an erasure is
called overdue: ninety-six attempts before the alarm. Not against load, because
an empty sweep is two queries per effect.

The cron expression carries **six fields**, not the familiar five. DBOS builds
its parser with seconds enabled, so `*/15 * * * *` is refused outright:

```text
"*/15 * * * *"     REJECTED: expected exactly 6 fields, found 5
"0 */15 * * * *"   ok, next two: 03:15:00  03:30:00
```

A test parses the constant with the same parser DBOS builds, so a well-meaning
"fix" to the five-field form fails in CI rather than at worker start.

## If you would rather the platform owned the clock

Delete `k8s/worker.yaml`, run `brain reap` from a `CronJob`, and change four
defaults:

- **`backoffLimit: 0`.** The default is 6. `reap` exits non-zero when an
  erasure has been outstanding for a day, which no retry can fix — and the
  claim leases for five minutes, so a retry inside that window claims nothing.
  The next schedule is the retry.
- **`concurrencyPolicy: Forbid`.** Overlap is safe; this stops runs piling up
  when the object store is hanging.
- **`startingDeadlineSeconds`.** Unset, a controller outage past 100 missed
  schedules stops the `CronJob` scheduling permanently.
- **`failedJobsHistoryLimit`** above its default of 1, so a failure is still
  readable the next morning.

You lose backfill and gain a failed Job, which is a louder signal than a log
line. That is the real trade between the two routes.

Either way, migrations stay in the brain Deployment's init container. Two
things owning the schema means a rollback can leave a schedule or a worker
running against a version the server no longer expects.

## The alarm

The age of the oldest tombstone, never the count. Nine hundred a minute old is
a busy delete; one a day old is a sweep failing every run.

How that surfaces depends on which route you took:

```text
brain reap    non-zero exit, and a failed Job if a CronJob ran it
brain worker  an ERROR log naming the oldest item, and the restart count
```

The worker's signal is weaker, and closing that gap means giving it a `/readyz`
that goes unhealthy on an overdue backlog. Not built yet.

## Why DBOS, and not Temporal

The full comparison with its measurements is `Tech Design/HLD-V1/Background-Work.md`
in the ideation repository. The short version, measured on one machine:

```text
                      binary cost    extra process    extra database
DBOS                    +1.88 MB          none             none
Temporal               +18.26 MB      4 services         2 databases
```

```text
temporal server start-dev   idle 110.0 MB   peak 258.4 MB
DBOS (whole application)    idle  12.5 MB   peak  25.6 MB
```

Temporal is a good engine whose floor is too high for a deployment we want to
keep possible: authentik alone is 1,122 MB across three containers, and Airbyte
— which ships Temporal built in — asks for 8 GB minimum. DBOS is a library. It
runs inside the process that already exists, against the Postgres that already
exists, and adds eleven tables in a schema of its own.

That last point is worth stating plainly: **`brain migrate up` is no longer the
only thing that changes the database.** DBOS creates and migrates its own
schema at `Launch`.

Held against it, and recorded rather than buried: the Go SDK is v1.2.0, the
project is backed by one vendor, and no project in the survey uses it yet —
Airbyte and AutoKitteh ship Temporal, while Grafana, Gitea, Mattermost and
Sourcegraph hand-roll a periodic runner. If it disappoints, the fallback is
`brain reap` from a `CronJob`, which is why that path is kept working.

## The tombstones are an outbox, not a queue

Worth stating, because it decides what can be built on top.

A broker's row means "this happened, whoever cares should look." A queue's row
means "run this job with these arguments." A tombstone means "Postgres has
already committed a change another system has not caught up to yet."

`deleted_objects` holds an object key, a team id, a timestamp and an attempt
count. No payload, no job type, no dead-letter table, no routing — the row *is*
the instruction, and it has exactly one consumer forever. That is what makes
deletes commute, `SKIP LOCKED` safe without a leader, and a replay free.

So an agent runtime cannot be built on it. When one arrives it becomes a second
job on this worker, with its own durable workflow, and two requirements that
are already knowable: it must enqueue inside the transaction that writes the
message, and a crash mid-run must not re-pay the tokens.
