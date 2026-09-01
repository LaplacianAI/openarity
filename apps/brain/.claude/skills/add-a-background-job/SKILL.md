---
name: add-a-background-job
description: Add work to the brain that runs on a schedule or off a queue rather than on a request — a periodic sweep, a durable multi-step run, an agent turn. Covers where a Job lives, the two phases DBOS forces on it, pinning names that are stored state, wiring it into cmd/brain, and the tests that catch a job that registers cleanly and never fires. Use whenever something must happen without a caller waiting for it.
---

# Add a background job

`brain worker` hosts jobs. One process, many `Job`s, one durable runtime
underneath — see `deployment/SCHEDULING.md` for what runs it and why DBOS is a
library rather than a cluster.

A job is not done until it is in the `worker.New(...)` call and a test drives
the wired object. A job that registers cleanly and never fires looks identical
to one that works.

## Step 0 — is it a job?

Foreground work fails visibly, once. Background work retries until it succeeds.
Move work here when you would rather it be slow than lost — and when nobody is
holding an HTTP request open for it.

Not a job: anything a handler can finish inside its own transaction. Reach for
a job when the work touches a system Postgres cannot include in a transaction,
or when it may take longer than a caller will wait.

## Step 1 — the file, beside the module it belongs to

```text
internal/reaper/job.go      the reaper's sweep
internal/runtime/job.go     agent runs, when they land
```

**Never a shared `internal/jobs` package.** It would have to import every
domain and call into it, so each domain exports its guts to one consumer — or
the logic drifts into `jobs/` and the domain goes hollow. The "see every
background job in one place" argument is already answered by the
`worker.New(...)` call in `cmd/brain/worker.go`, one line per job.

Same answer `internal/api/<domain>` and `internal/gateway/<provider>` already
got: the domain owns its logic, the edge owns the wiring.

One file may hold several jobs. Split when it stops being readable, not by
policy.

## Step 2 — the two phases, which are not a style choice

```go
type Job interface {
	Name() string
	Register(d dbos.Context) ([]dbos.ScheduleSpec, error)
}
```

`Register` runs **before** the runtime launches, because DBOS builds its
workflow and queue registries in memory and reads them at `Launch`. The
schedules it returns are applied **after**, because installing one needs a
running scheduler. Getting this backwards fails at runtime, not at compile
time.

A job with nothing periodic returns `nil` — that is the event-driven shape, and
it must not be mistaken for a failure.

`internal/worker` never imports a domain, and a domain never imports
`internal/worker`. Go interfaces are structural: declare the two methods and
the job satisfies `worker.Job` by shape. Only `cmd/brain` knows both.

## Step 3 — names are stored state

```go
const (
	sweepWorkflow = "openarity.reaper.sweep"
	sweepSchedule = "reaper-sweep"
)

dbos.RegisterWorkflow(d, j.sweep, dbos.WithWorkflowName(sweepWorkflow))
```

**Always `WithWorkflowName`.** DBOS derives a workflow's name from
`runtime.FuncForPC` otherwise, so renaming the method or moving the package
renames the workflow and orphans the schedule already in the database. Nothing
errors; the schedule simply points at a workflow nobody registered, and stops
firing.

The same constant goes in `ScheduleSpec.WorkflowName`. A closure is safe once
the name is pinned and unsafe before.

## Step 4 — `ApplySchedules`, never `CreateSchedule`

`CreateSchedule` is not idempotent. A worker restarting against its own
schedule gets:

```text
failed to create schedule: ERROR: duplicate key value violates unique
constraint "workflow_schedules_schedule_name_key" (SQLSTATE 23505)
```

Measured, and it only appears on the **second** run — the first deploy looks
perfect. `internal/worker` collects every job's specs and applies them once, so
a job should return specs rather than install them.

## Step 5 — the cron expression has six fields

DBOS builds its parser with `cron.WithSeconds()`, so the familiar five-field
form is refused outright:

```text
"*/15 * * * *"     REJECTED: expected exactly 6 fields, found 5
"0 */15 * * * *"   ok, next two: 03:15:00  03:30:00
"@every 15m"       ok, but unaligned to the clock
```

Keep the expression a constant with a test that parses it — see step 8 — so the
refusal lands in CI rather than at worker start.

`SchedulerPollingInterval` defaults to **30 seconds**, which is how long a
newly created schedule can take to install. A test window shorter than that
measures the interval, not the schedule.

Set `AutomaticBackfill: true` unless there is a reason not to. It replays the
ticks missed while the process was down, which is the one thing a Kubernetes
`CronJob` cannot do.

## Step 6 — wire it into `cmd/brain/worker.go`

```go
return worker.New(cfg.PostgresDSN, logger,
	reaper.SweepJob(logger, effects...),
	runtime.AgentRunsJob(...),
).Run(ctx)
```

One constructor per job, listed individually. A module returning `[]Job` cannot
be spread here — Go will not convert a slice of one interface type to another
even when every element satisfies both — and the individual form keeps this
call a complete index.

Build dependencies **before** `Run`, in `cmd/brain`, so a misconfigured store
fails at boot rather than fifteen minutes later inside a workflow where the
error is a row in a table.

## Step 7 — what a long-lived process must not do

- **Do not make a recoverable alarm fatal at startup.** `brain reap` exits
  non-zero on an overdue erasure because a one-shot command's exit code is its
  report. A worker doing the same deadlocks: it refuses to start, so the sweep
  never runs, so the backlog stays overdue. Log it loudly and carry on. Same
  error, opposite handling.
- **Do not assume one replica.** Anything claiming work needs `FOR UPDATE SKIP
  LOCKED` or an equivalent. Two workers are then a division of labour rather
  than a race, and no leader election is needed — which is only true because
  the work commutes. Work that does not commute needs a partition key and one
  worker per partition.
- **Do not rely on `LISTEN`/`NOTIFY` alone.** Measured: a notification sent
  while the listener was down is gone, and the payload is capped at 8000 bytes.
  Push cuts latency; the periodic poll is what makes the work happen at all.

## Step 8 — the tests

`internal/<module>/job_test.go` and, for the registry, `internal/worker/`.
Every job owes at least these:

1. **The cron parses, with the parser DBOS actually builds.** And a second test
   asserting the five-field form is refused, so the six-field constant is shown
   to be load-bearing.
2. **The spec is what it claims** — schedule name, workflow name, and
   `AutomaticBackfill`. Split spec construction out of `Register` into its own
   method: `dbos.RegisterWorkflow` panics on a nil context, so a spec only
   reachable through `Register` is a spec no cheap test reads.
3. **A job with nothing to do refuses.** No effects, no handlers, no work —
   registering anyway produces a schedule that fires forever and does nothing,
   and looks healthy doing it.
4. **Two jobs cannot claim one schedule name**, and neither can one job twice.
   `ApplySchedules` takes the last spec for a name, so the loser registers, logs
   a clean startup, and never fires.

Then the wiring: the job appears in `worker.New(...)`, and
`TestEveryCommandIsRoutedFromExecute` covers the command reaching it.

## Step 9 — verify

```sh
cd apps/brain && make check && make cover
```

Then break each guard and watch a test fail — the cron to five fields,
`AutomaticBackfill` to false, the duplicate check deleted. A job is the kind of
thing that passes every check while doing nothing at all.
