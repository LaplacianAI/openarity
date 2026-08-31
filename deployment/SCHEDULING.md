# Running the reaper on a schedule

Deleting a team, a channel or a person removes their rows. It cannot remove the
attachment bytes in the object store or the secrets in the vault, because
Postgres has no transaction that spans them. Each deletion records what it owes
instead — a tombstone written by a trigger, in the same transaction that
removed the row — and `brain reap` completes it.

A deployment that never runs `reap` never erases anything outside Postgres.
This file is how to make sure it runs, and why the answer is not the same on a
2 GB VPS as it is in an enterprise cluster.

## The three tiers

| Tier | Scheduler | What it costs |
| --- | --- | --- |
| Personal, one host | `ticker` — in process, inside `brain worker` | nothing |
| Kubernetes | `external` — a `CronJob` runs `brain reap` | nothing |
| Enterprise | `temporal` — a Temporal Schedule fires the sweep | two databases, four services, +16 MB |

**Today only the middle row exists.** `brain reap` is built and tested; a host
`cron`, a systemd timer or a Kubernetes `CronJob` drives it. `brain worker` and
`OPENARITY_SCHEDULER` are the decided shape, not shipped code — this file is
the record of that decision and the measurements behind it, written before the
implementation so the implementation is argued rather than assumed.

`brain reap` stays a standalone one-shot command in every tier. That is
deliberate: the scheduler integration can be wrong without erasure stopping,
because the command can always be run by hand or from a host `cron`. A hard
dependency that can strand a legal obligation deserves a manual path beside it,
and here that path is the code we already ship.

## Why this is a seam and OIDC is not

The brain speaks OIDC, so authentik is a reference deployment rather than a
requirement — swap in Dex, Okta or Entra by changing one environment variable
and the brain never knows. That works because OIDC is a *specification*.

There is no specification for scheduling or durable execution, and the
candidates do not agree on a model:

- **River** — `Work(ctx, job) error`. Runs to completion; a failure retries
  from the top.
- **Temporal** — workflow code must be deterministic and replayable; side
  effects live in activities.
- **DBOS** — annotated functions with Postgres checkpoints.
- **Restate** — journaling through `ctx.run()`.

An interface all four satisfy collapses to "run this function," which discards
the replay that was the reason to pick Temporal at all. So the seam is ours:
a small `Scheduler` port with adapters we write and a conformance kit, on the
pattern of `internal/gateway`, rather than a thin wrapper over a standard that
does not exist.

## What the tiers cost, measured

Memory, `docker stats` against a running local stack:

```text
redis                  12.85 MiB   (running, unused by the brain)
openbao                35.94 MiB
falkordb               86.24 MiB   (running, unused by the brain)
minio                 104.4  MiB
authentik-postgresql  240.5  MiB
postgres              334    MiB
authentik-worker      376.2  MiB
authentik-server      504.9  MiB
```

On a 2 GB host that leaves roughly 660 MB for a lean core — OS and Docker,
Postgres, OpenBao, the brain. A Temporal cluster on top fits only in the sense
that the arithmetic works: it does not leave headroom, and Postgres would be
serving the application and Temporal's per-event writes at once.

Note also that authentik alone is 1,122 MB across three containers. A 2 GB
deployment is using a hosted issuer or a single-binary one long before Temporal
is the thing ruling it out.

Binary, built with the flags in `deployment/Dockerfile` — linux/amd64,
`-trimpath -ldflags="-s -w"`, CGO off:

```text
without the Temporal SDK   14,573,730 bytes
with it                    30,953,634 bytes   (+16.4 MB, 2.12x)

module graph (go list -m all)   112 -> 150
```

The SDK brings gRPC and protobuf, neither of which the brain links today. The
megabytes are cheap; the 38 extra modules in the build graph are the part worth
weighing, because they are 38 more things to track for advisories. If that
becomes the deciding factor, a `//go:build temporal` tag drops the SDK from a
default build — worth doing to working code, not designing around in advance.

## Temporal, if you run it

Three deployment shapes, only one of which is for production:

| | What runs | Persistence | Production |
| --- | --- | --- | --- |
| `temporal server start-dev` | one process, Web UI, default namespace | in-memory SQLite, or `--db-filename` | no |
| `temporalio/auto-setup` | all services in one process, schema auto-init | your Postgres | no |
| `temporalio/server` | frontend, history, matching, worker | Postgres, MySQL or Cassandra; schema applied by hand | yes |

Temporal's own compose repository says so directly: those setups "do not use
Temporal Server directly — they utilize an `auto-setup` script," and points at
`temporalio/server` for production with dependencies managed separately.

Things worth knowing before committing to it:

- **It needs two databases of its own** — Executions and Visibility. Not tables
  in ours; its own schema, its own migration tool, its own upgrade cycle. The
  documentation recommends keeping the two apart, because large visibility
  scans otherwise block writes to the workflow tables.
- **Elasticsearch is optional** since Server v1.20 — advanced visibility works
  on Postgres. It is also the memory-hungry part of the default stack, so
  leaving it out is most of the saving.
- **Every workflow event is a persisted write** — roughly 5 ms p50 on Postgres,
  around 500 events/sec per connection. That is the mechanism rather than a
  flaw, and it is irrelevant at our volumes: a sweep every fifteen minutes is a
  handful of events a day.
- **Only the Temporal parts of the Helm chart are production-ready.** Its
  bundled Cassandra, Elasticsearch, Prometheus and Grafana are development
  configurations.

The cost is a floor, not a slope: two databases and four services are the same
price for one workflow a day as for a million an hour. That is why the reaper
alone does not justify it and an agent runtime would.

## Kubernetes, where the defaults are wrong

A `CronJob` running `brain reap` needs four settings changed:

- **`backoffLimit: 0`.** The default is 6. `reap` exits non-zero when an
  erasure has been outstanding for a day, which no retry can fix — and the
  claim query leases for five minutes, so a retry inside that window claims
  nothing and does nothing. The next schedule is the retry.
- **`concurrencyPolicy: Forbid`.** Overlap is safe — the claim uses `FOR UPDATE
  SKIP LOCKED` — so this is not correctness. It stops runs piling up when the
  object store is hanging.
- **`startingDeadlineSeconds`.** Left unset, a controller outage past 100
  missed schedules stops the `CronJob` scheduling permanently.
- **`failedJobsHistoryLimit`** above its default of 1, so a failure is still
  readable the next morning.

Migrations stay in the Deployment's init container and are not repeated in the
`CronJob`. Two things owning the schema means a rollback can leave a schedule
running against a version the application no longer expects. The cost is that
on a fresh install the `CronJob` may fire before the Deployment has migrated
and fail once, which the next tick clears.

## The interval

Fifteen minutes. Sized against the 24-hour threshold at which `reap` calls an
erasure overdue — 96 attempts before the alarm — rather than against load. An
empty sweep is two queries per effect; the real per-run cost is process start,
a Postgres connection, and an AppRole login.

The alarm is the age of the oldest tombstone, never the count. Nine hundred a
minute old is a busy delete; one a day old is a sweep failing every run.

## What was considered and not chosen

- **River** — a Postgres job queue with `InsertTx`, which is the transactional
  enqueue an agent runtime will need. It retries from the top rather than
  checkpointing, so it does not solve the expensive half. 21 modules.
- **asynq** — Redis-backed, so it cannot join a Postgres transaction. Adopting
  it for the agent runtime would reintroduce exactly the dual write the
  tombstones exist to remove. Redis running unused in compose is not a hint
  about this.
- **robfig/cron** — the ecosystem default and unmaintained: last release
  v3.0.0 in 2019, with 116 issues and 57 pull requests open. `netresearch/go-cron`
  is an actively maintained drop-in if cron *expressions* are ever wanted; a
  duration is the right knob for "without undue delay."
- **go-co-op/gocron** — maintained, and its headline feature is distributed
  locking so replicas do not double-fire. `SKIP LOCKED` already gives us that.
- **ofelia** — schedules by talking to `/var/run/docker.sock`. Mounting a
  root-equivalent socket to schedule an erasure job is the wrong trade.
- **supercronic** — a crontab-compatible runner built for containers, and the
  closest thing to a no-code answer for compose. It logs a failed job and keeps
  going, which trades away the property the exit code was chosen for.

A shell loop in the container is not an option either: the image is
`gcr.io/distroless/static-debian12`, which has no shell.

```text
docker run --entrypoint /bin/sh ghcr.io/laplacianai/openarity-brain:latest
stat /bin/sh: no such file or directory
```

## The tombstones are an outbox, not a queue

Worth stating plainly, because the difference decides what can be built on top.

A broker's row means "this happened, whoever cares should look." A queue's row
means "run this job with these arguments." A tombstone means "Postgres has
already committed a change another system has not caught up to yet."

`deleted_objects` holds an object key, a team id, a timestamp and an attempt
count. No payload, no job type, no dead-letter table, no routing — the row *is*
the instruction, and it has exactly one consumer forever. That is what makes
deletes commute, `SKIP LOCKED` safe without a leader, and a replay free.

So an agent runtime cannot be built on it. When one arrives it will need a real
queue, and two requirements are already knowable from code that exists:

- **It must enqueue inside the transaction that writes the message.** A message
  and its attachments already commit together under `InTx`. "Message stored but
  run never enqueued" is the same dual write in a new place.
- **A crash mid-run must not re-pay the tokens.** This is the requirement no
  Postgres job queue meets and durable execution exists for — though the
  comparable open-source agent runtimes make the *ledger* durable and let the
  run be re-runnable instead, which is a defensible answer and a cheaper one.

Temporal cannot satisfy the first: starting a workflow is an RPC, not a row in
our transaction. It sits behind an outbox rather than replacing one.
