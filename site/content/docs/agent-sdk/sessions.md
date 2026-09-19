---
title: Sessions
weight: 8
---

Steering a run no process is holding, and resuming one whose process has gone.

## What breaks with two replicas

`Run.Steer` is a method on a handle:

```go
run := runner.Start(ctx, spec, msgs, endpoint, events)
run.Steer("check the lease expiry, not the token")
```

That handle lives in the memory of the process that called `Start`. Put a
second replica behind a load balancer and a steer posted to the API reaches
the run about half the time. Kill that replica and the run is gone with it —
not just the answer, but the turn's work.

Neither is a bug in the loop. Both are the same missing thing: nowhere outside
the process holds what the run knows.

## Two interfaces

A `Store` is a backend. A `Session` is one conversation, already addressed, so
nothing in the loop threads an id around.

```go
type Store interface {
	Save(ctx context.Context, session string, msgs []Message) error
	Messages(ctx context.Context, session string) ([]Message, error)
	AddSteer(ctx context.Context, session, text string) error
	TakeSteers(ctx context.Context, session string) ([]string, error)
}

type Session interface {
	Save(ctx context.Context, msgs []Message) error
	Messages(ctx context.Context) ([]Message, error)
	Steer(ctx context.Context, text string) error
	TakeSteers(ctx context.Context) ([]string, error)
}
```

Open one and hand it to the spec:

```go
store, err := sqlite.Open("sessions.db")
session, err := agent.Open("conversation-1", store)

spec := agent.Spec{
	Pattern:  agent.PatternReAct,
	MaxSteps: 8,
	Session:  session,
}
```

`Spec.Session` is nil by default and costs nothing when it is — no call, no
allocation, no change in behaviour. It is the equivalent of the Claude Agent
SDK's `persistSession: false`, except that nothing is the default rather than
something.

## Steering from somewhere else

With a session, a steer does not need the run at all:

```go
session, err := agent.Open("conversation-1", store)
session.Steer(ctx, "check the lease expiry, not the token")
```

Nothing here holds a `*Run`. The process that started the run may be on
another machine, and the run may not have started yet.

The loop collects whatever is waiting immediately before every model request,
and a steer from the store goes into the same place `Run.Steer` writes to — so
it lands where the run had got to, not at the end of the conversation. That
matters more than it sounds: a steer pinned last is re-read as a fresh
instruction on every turn, and the model never concludes.

`Run.Steer` still works and still costs no round trip. Use it when you are
holding the run; use the session when you are not.

{{< callout type="warning" >}}
`Result.UnappliedSteers` keeps its old meaning: steers that reached the
in-memory box and never made it onto a request. Once a session is in play it
is no longer the complete list of what is undelivered — a steer still sitting
in the store was never taken, so it stays there and the next run collects it.
{{< /callout >}}

## Resuming

There is no resume call, because `Runner.Run` has always taken messages:

```go
msgs, err := store.Messages(ctx, "conversation-1")
result, err := runner.Run(ctx, spec, msgs, endpoint, events)
```

The replica doing this never ran the turn that produced those messages. It
does not need a session of its own to carry on from them.

## What a crash costs

The transcript is written before every model request, and once more when the
pattern returns — the model's last reply only appears in the *next* request,
so a save per request would never write the answer.

That sets the granularity: **a run that dies mid-step resumes from the step
before and redoes the one it was in, tool calls included.**

{{< callout type="warning" >}}
A tool with a side effect can therefore run twice. A message sent, a ticket
filed. Nothing here prevents that, and a deployment whose tools are not
idempotent should know it.

This is LangGraph's behaviour too — resuming re-runs the whole node rather
than continuing after the `interrupt()` — so it is a known shape rather than a
novel risk.
{{< /callout >}}

## The stores

| Package | Backend | Give it |
| --- | --- | --- |
| `stores/memory` | nothing; lives as long as the process | — |
| `stores/sqlite` | one file | a path |
| `stores/postgres` | Postgres | a `*pgxpool.Pool` |
| `stores/mysql` | MySQL | a `*sql.DB` |
| `stores/mongodb` | MongoDB | a `*mongo.Database` |

Each takes the connection you already have rather than a DSN. Where the
database is, how many connections to hold and how long to wait are decisions
the SDK has no business making — and a service that already has a pool must
not open a second one against the same database, or its connection limits stop
meaning anything.

The drivers live in subpackages so the core cannot link them: `make boundary`
fails if it does. Importing `agent` costs you no database driver at all.

### Migrations are yours

`stores/postgres` and `stores/mysql` export their DDL and run none of it:

```go
_, err := pool.Exec(ctx, postgres.Schema)       // Postgres: one string
for _, stmt := range mysql.Schema { … }         // MySQL: one statement each
```

A library that silently migrates a shared database is a library that will one
day do it during an incident. `stores/sqlite` is the exception and creates its
tables on open — one file and one process means there is nothing to coordinate
with.

### They behave the same, and that is tested

Every store runs the same conformance suite, `stores/storetest`. A store
author's whole test file is one call to `Run`:

```go
func TestMyStoreKeepsTheContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) agent.Store { return myStore() })
}
```

It is not ceremony. Writing it is what found MySQL's `JSON` column and
Postgres's `jsonb` quietly rewriting what they were given:

```text
stored    {"z":1,"a":[1, 2],"z":3}
jsonb  →  {"a": [1, 2], "z": 1}
```

Reordered, respaced, and the duplicate key gone without a word — on one
backend only, while SQLite and memory kept the bytes. A model's tool arguments
are raw JSON it produced, so every store now keeps them verbatim and the suite
says so.

## Example

`examples/sessions` is three runners over one SQLite file and nothing else —
not a channel, not a mutex, not a `*Run`:

```text
replica B   left a steer in sessions.db, holding no run — there was none yet
replica A   ran 2 steps and was never told replica B existed
transcript  4 messages, and the steer is message 0 of them
replica C   resumed from 4 messages it never produced, and answered
            "renewLease returns a nil lease when the lease has expired…"
steers      0 left in the store: it was handed over exactly once
```

## Not built

- **Forking a conversation**, and resuming at a chosen message — the Claude
  Agent SDK's `forkSession` and `resumeSessionAt`.
- **Tool-level journalling**, which would stop a tool re-running after a
  crash.
- **Knowing which replica holds a run**, and stopping it. Steering and resume
  work without that; cancelling does not.
