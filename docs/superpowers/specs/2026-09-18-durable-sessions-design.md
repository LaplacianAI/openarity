# Durable sessions and steering across a process boundary

**Status:** design, not built
**Module:** `sdk/agent`, with a wrapper in `apps/brain`

## The problem

A run lives in the memory of the process that started it.

`Run.Steer` is a method on a handle held in one process. With two replicas
behind a load balancer, a steer posted to the API reaches the run about half
the time. Kill that replica and the run is gone with it — not just the answer,
but the turn's work.

Neither is a bug in the loop. Both are the same missing thing: nowhere outside
the process holds what the run knows.

## What belongs where

"Session" does three jobs, and they do not live in the same place.

| | What it is | Where |
| --- | --- | --- |
| Ownership | team, channel, `provider_ref`, who may read it | brain |
| History | the ordered messages | SDK interface, pluggable backend |
| Liveness | steers waiting to be delivered | SDK interface, pluggable backend |

Ownership never moves. `sdk/agent` is a separate module so that an
authorisation decision cannot leak into the loop — the compiler refuses the
import rather than a reviewer noticing. A `team_id` inside the SDK undoes that
in one field.

History and liveness are the same store, written at the same moments. A run's
transcript at step N *is* the conversation's history so far; building resume
and session history separately would be building one thing twice.

### Why the SDK owns the timing

The SDK is the only thing that knows *when* to read and write. A steer must be
collected immediately before a model request, because its position in the
transcript is pinned to `len(msgs)` at that moment:

```go
func (c *steeringClient) apply(msgs []Message) []Message {
	fresh, carried := c.box.take(len(msgs))
	...
	return recordSteers(msgs, carried)
}
```

That pinning is not incidental. A steer pinned anywhere else — appended last,
say — makes the model re-read it as a fresh instruction every turn and never
conclude: five steps, four tool calls, no answer. Moving the polling into the
brain means reimplementing the pinning, the continuation loop and
`UnappliedSteers` outside the package that has tests for all three.

So the brain supplies a place to put things. The SDK decides when.

### Every comparable SDK agrees

| SDK | Session in the library | Storage | Pluggable |
| --- | --- | --- | --- |
| Claude Agent SDK | `sessionId`, `resume`, `forkSession`, `resumeSessionAt`, `persistSession` | JSONL under `~/.claude/projects/` | no |
| OpenAI Agents SDK | `Session` | SQLite, Redis, Mongo, SQLAlchemy | yes |
| Agno | `session_id` + `db` | `agno_sessions` and other backends | yes |
| LangGraph | checkpointer + `thread_id` | `InMemorySaver`, `PostgresSaver` | yes |

Four for four put the concept in the library. Three of four make the backend
pluggable; the Claude Agent SDK is the exception because it is one user on one
machine and can name a path. Ours cannot — the same library serves a
multi-tenant brain and somebody's standalone Go program.

Worth noting what the Claude Agent SDK does with a live session:

```ts
interface SDKSession {
  readonly sessionId: string
  send(message: string | SDKUserMessage): Promise<void>
  stream(): AsyncGenerator<SDKMessage, void>
  close(): void
}
unstable_v2_resumeSession(sessionId, options): SDKSession
```

Resuming returns a handle you can `send()` into. Steering and resume are one
feature there, not two. The design below keeps that property.

## The interfaces

Two, not one: a backend that can have several implementations, and the
conversation concept built on top of it.

```go
// Store is a backend. It knows nothing about agents — it holds a transcript
// and a queue of undelivered steers, addressed by a session id it never
// interprets.
type Store interface {
	Save(ctx context.Context, session string, msgs []Message) error
	Messages(ctx context.Context, session string) ([]Message, error)
	AddSteer(ctx context.Context, session, text string) error
	TakeSteers(ctx context.Context, session string) ([]string, error)
}

// Session is one conversation, already addressed. This is what the runner
// talks to, so nothing in the loop threads an id around.
type Session interface {
	Save(ctx context.Context, msgs []Message) error
	Messages(ctx context.Context) ([]Message, error)
	Steer(ctx context.Context, text string) error
	TakeSteers(ctx context.Context) ([]string, error)
}

// Open binds an id to a store. A caller with something exotic can implement
// Session directly instead.
func Open(id string, store Store) (Session, error)
```

Each `Store` method is one statement against a database. `Save` takes the
transcript as it stands rather than a delta, because steers are inserted at
pinned positions rather than appended — a delta would have to describe an
insertion, and the interface stops being one statement. An implementation that
minds the cost may diff; the interface does not require it.

`TakeSteers` is destructive by contract: a steer is returned exactly once —
`DELETE … RETURNING` in SQL terms.

### The stores that ship

| Package | Driver | Who it is for |
| --- | --- | --- |
| `agent` (core) | none | `MemoryStore`, for tests, examples and a program that wants session semantics without a backend |
| `agent/stores/postgres` | `jackc/pgx/v5` | the brain, and anyone already running Postgres |
| `agent/stores/sqlite` | `modernc.org/sqlite` | a single machine, a laptop, a test that wants a real database |

More backends later; the four methods are the whole contract.

`modernc.org/sqlite` rather than `mattn/go-sqlite3` because it is pure Go. A
cgo driver breaks `CGO_ENABLED=0` and cross-compilation, which a library has
no business imposing on whoever imports it.

The drivers live in subpackages for the same reason `models/openaicompat` and
`tools/mcp` do: the core must not be able to see them. `make boundary` is what
enforces that, and its comment already says so —

> Adding an adapter under `models/` or `tools/` means adding a line here,
> because the point of those directories is that the core cannot see into them.

So this change adds two `FORBIDDEN` entries, and `stores/` joins that list of
directories. `MemoryStore` stays in the core precisely because it links
nothing.

### Creating the schema

The two differ, and the difference is honest rather than an oversight:

- **SQLite** creates its tables when the store is opened. One file, one
  process, nothing to coordinate.
- **Postgres** exports its DDL and runs nothing. A library that silently
  migrates a shared database is a library that will one day do it during an
  incident. The brain applies it through the migration system it already has,
  with the advisory lock it already takes.

## Wiring

One new field on the spec:

```go
type Spec struct {
	...
	Session Session   // nil means in-memory only, exactly as today
}
```

`nil` is the whole of the opt-out. There is no flag, and nobody pays for a
feature they have not asked for — the equivalent of the Claude Agent SDK's
`persistSession: false`.

The store plugs in at the seam every other cross-cutting feature uses. The
current chain, outermost first:

```text
countingClient → [outputClient] → steeringClient → client
```

becomes

```text
countingClient → [outputClient] → steeringClient → savingClient → client
```

`savingClient` sits *inside* `steeringClient` deliberately: it therefore sees
`req.Messages` with the steers already inserted, so a steer becomes durable in
the same instant it is taken. Without that ordering a steer taken from the
store and then lost to a crash would be gone.

### Reading a steer

`steeringClient.apply` gains a fetch ahead of the existing take:

1. `session.TakeSteers(ctx)` — anything another replica left
2. each of them into the in-memory box, as `Run.Steer` already does
3. `box.take(len(msgs))` — unchanged
4. `recordSteers` — unchanged

Everything after step 2 is the code that exists now. `Run.Steer` still writes
straight to the box, so steering a run you are holding costs no round trip.

`apply` needs a context and an error return, so `Complete` and `Stream`
propagate both.

### Writing the transcript

Two moments, both `Save`:

- **Before each model request**, from `savingClient`. This is what makes resume
  possible at all, and it is what sets the granularity below.
- **When the pattern returns**, in `Runner.run`, once per turn of the
  continuation loop. The model's last reply only ever appears in the *next*
  request, so without this the final answer is never written.

### Resuming

There is no resume call. The brain reads its own table and passes the messages
to `Runner.Run`, which has always taken messages:

```go
msgs, err := store.Messages(ctx, sessionID)
result, err := runner.Run(ctx, spec, msgs, endpoint, events)
```

That is why `Session.Messages` exists for the caller's benefit and the loop
never calls it. Adding a resume entry point would be a second way to do what
`Run(msgs)` already does.

## What a crash costs

Saving happens once per model request, so a run that dies mid-step resumes
from the previous step boundary and **redoes the step it was in — tool calls
included**.

The consequence is stated plainly because it is real: a tool with a side
effect can run twice. A message sent, a ticket filed. Nothing in this design
prevents that, and a deployment whose tools are not idempotent should know it.

This is LangGraph's behaviour too — resuming re-runs the whole node rather than
continuing after the `interrupt()` — so it is a known shape rather than a
novel risk. Tool-level journalling, which would fix it, is deliberately out of
scope; the interface above does not have to change to add it later, because
"how often `Save` is called" is not part of the contract.

Also accepted, and for the same reason: a steer taken in the same instant the
process dies is lost. The window is one model request wide, and the save
ordering above makes it as small as it can be without a second round trip.

## Refusing what cannot work

There is no `checkSession` in the runner, because there is no combination of
spec fields that contradicts itself: `Spec.Session` is either nil or a
`Session`, and nil is the default rather than an error.

The one refusal lives in `Open`, which returns an error rather than a `Session`
when the id is empty — a store addressed by `""` would quietly collect every
session in one bucket, which is worse than failing.

## Errors

A failure from `TakeSteers` or `Save` fails the model request, and therefore
the run.

The alternative — log it and carry on — is worse in both directions: a dropped
steer looks delivered to whoever sent it, and a skipped save silently removes
the durability the caller asked for by setting `Spec.Session` at all. A run
that fails can be retried from the last save; a run that quietly lost a steer
cannot be detected.

## Unapplied steers

`Result.UnappliedSteers` keeps its current meaning: steers that reached the
in-memory box and never made it onto a request. Steers still sitting in the
store were never taken, so they stay there and the next run collects them.
That is the right outcome and needs no code — but it means `UnappliedSteers`
is not a complete list of what is undelivered once a store is in play, and the
documentation has to say so.

## The brain's wrapper

The brain does **not** implement `agent.Store`. It wraps the one that ships:

```go
store := postgres.New(pool)                       // agent.Store
session, err := agent.Open(row.ID.String(), store)
spec.Session = session
```

What the brain adds around it is the part the SDK must never have: the
authorisation check that decides whether this caller may address this session
at all. That happens before any of the above is reached, and a caller who may
not see a session still gets a 404 rather than a 403, exactly as today.

So the wrapper is id mapping plus the guard. No second implementation of the
four methods, and no `team_id` anywhere below `agent.Open`.

### The brain already has a `messages` table, and it is not this

This will look like duplication and is not. `messages` holds what a **provider
sent**:

```text
channel_id, user_id, external_id, conversation_ref, text, sent_at, received_at
```

No role, no tool calls, no assistant replies, and a uniqueness constraint on
`external_id` because every provider retries a slow webhook. It is the record
of what arrived from Slack.

The agent transcript is a different thing — roles, tool calls, tool results,
the model's own words — and it belongs in the store's own tables. Merging them
would mean either putting `external_id` on an assistant message or putting tool
calls on an inbound webhook, and both are wrong.

One new migration in the brain, applying the Postgres store's exported DDL,
with a `Down`.

## Testing

In `sdk/agent`:

- a steer written to the store reaches the next request, and lands at the
  pinned position rather than the end
- a steer is delivered once: a second request does not carry it again
- the transcript is saved before each model request, and once more when the
  pattern returns
- a store that fails `TakeSteers` fails the run rather than dropping the steer
- a store that fails `Save` fails the run
- `Spec.Session == nil` touches no store and behaves exactly as today —
  asserted against a store that fails every call
- `Run.Steer` on a run with a session still works without a round trip
- every shipped store satisfies the same contract, driven from one table:
  `MemoryStore`, SQLite against a temp file, and Postgres when a DSN is set —
  including `TakeSteers` returning each steer exactly once under two
  concurrent callers

Each guard gets broken to confirm a test fails without it.

In `apps/brain`: that a caller who may not see a session never reaches the
store at all, and that the session id the brain passes to `agent.Open` is the
row id and carries nothing else.

## Not in scope

- **`forkSession`** — branching a conversation. A real feature, not this one.
- **`resumeSessionAt`** — resuming at a chosen message. Same.
- **Tool-level journalling** — see above; addable without an interface change.
- **Run ownership and cancellation across replicas** — knowing *which* replica
  is running a session, and stopping it. This design makes steering and resume
  work without that; cancelling still needs it.
- **Compaction** — a transcript that outgrows a context window. Orthogonal,
  and it belongs wherever summarisation lands.
- **More backends** — Redis, Mongo, a file of JSONL. Postgres and SQLite are
  what ship; the contract is four methods, so the next one costs a package and
  a line in `FORBIDDEN`.
