# internal/gateway

An adapter turns one provider's webhook into `Inbound`. That is all it does:
no database, no secret store, no HTTP client, no authorisation.

The reason for the ceremony is who writes adapters and where they run.
Adapters are written against someone else's documentation, tested against
payloads copied from a blog post, often contributed by whoever wanted that
platform — and they sit on a public, unauthenticated URL that anyone on the
internet can POST to. So they are given as little power as possible, and what
is left is a function from bytes to structs, which can be tested exhaustively
without being trusted at all.

## Layout

```text
internal/gateway/
  message.go        Inbound, Session, Result, Validate — the normalised contract
  provider.go       Provider, WebhookRequest, Credentials, Registry
  handler.go        the request path: route, verify, parse, resolve, deliver
  senders.go        ResolveSender, and the display-name cleaning
  sink.go           where a delivery goes; inbox.go is the stand-in
  providertest/     the conformance suite every adapter runs
  custom/           the generic adapter, and the reference implementation
  <provider>/       one package per platform: slack, telegram, discord
```

Imports run one way. `custom` imports `gateway`; `gateway` never imports
`custom`. That is what makes `if provider.Name() == "slack"` in the shared path
impossible to write rather than merely discouraged — the package cannot be
named from there. Provider-specific behaviour has nowhere to live except inside
the provider's own package, and the compiler enforces it instead of a reviewer.

Wiring is `cmd/brain`, exactly as route packages are. Adding an adapter is one
line in `newRegistry` plus a package that passes `providertest`. The routes
mount themselves: `New` walks the registry and registers
`{Method} /hooks/{name}/{channel_id}{Suffix}` for every route every provider
declares.

The path chooses the adapter, so an unknown provider is a 404 from the mux
before any query runs — and the channel row's `provider` column is compared to
it, so a channel id posted to another provider's path is refused. Without that
comparison a channel id works on every provider's path at once, and the weakest
signature scheme among them is the one that counts.

Everything mounts on the **webhook** listener, never the API one. The two
authenticate on incompatible principles: an API route identifies a caller and
asks what they may do; a hook proves a signature over raw bytes and has no
caller at all. On the API mux every provider on earth is rejected for carrying
no bearer token.

## Adapters receive values, never capabilities

| It gets | Rather than | So it cannot |
| --- | --- | --- |
| `WebhookRequest` | `*http.Request` | read the socket, hijack the connection, write a response, leak the request context into a goroutine |
| `Credentials` | `secrets.Store` | read another channel's signing secret |
| `ReceivedAt` | `time.Now()` | be untestable — a recorded fixture would stop verifying the day after it was recorded |

`Keys()` is what makes the second row work: the adapter *declares* which secret
keys it needs and the handler *fetches* them, scoped to the one channel in the
URL. Same information, no authority. The tempting alternative — hand the
adapter a `secrets.Store` — is one method shorter and gives every adapter the
ability to read every other channel's credentials, in a call that looks
identical to a legitimate one.

The general form: **pass the data, not the thing that can obtain the data.**

## Parse is pure and offline

No network, no clock, no randomness, no database. This is not a style
preference — it is the property that lets `providertest` throw hostile bytes at
an adapter and assert it never dials out.

Go cannot express it in a type, so the suite enforces it by calling `Parse`
twice with the same bytes and requiring the same answer. An adapter reading a
counter or the clock fails in its own test, naming itself, rather than
surfacing as an intermittent handler failure three tasks away.

Consequences that follow from purity:

- **`Attachment` holds a `Ref`, not bytes.** Fetching happens afterwards,
  through the optional `Fetcher` interface. See below.
- **`Enrichment` is where deferred work goes.** It is defined by a constraint,
  not a category: everything `Parse` was forbidden from doing. Substitutions
  that need `channel_senders`, a place name that needs geocoding.
- **`Text` is never rewritten in place.** It stays as the provider sent it, so
  what arrived is still auditable after rendering.

## Result{} is "nothing to do"

Most of what a busy webhook receives is not a message — a reaction, an edit,
someone joining a room. The zero `Result` is the correct answer, not an error.

`Result.Ack` exists for one reason: Slack refuses to save a webhook URL until
the endpoint echoes its `url_verification` challenge back in the response. The
adapter *returns* the bytes and the handler writes them, so the adapter still
cannot set a status, stream, or hijack the connection. When one provider needs
an escape hatch, widen the return value, not the capability.

It is `Ack` and not `Reply` because `Reply` is the name the outbound seam will
want. The agent's answer is a long-running job — a workflow that finishes
minutes later and posts through the provider's API on a new connection. It
cannot ride this response, which is closed within seconds. Two directions, and
they are not symmetric: inbound is synchronous and mandatory, outbound is
asynchronous and optional.

## What each status code means

A provider retries on any non-2xx and eventually disables an endpoint that
keeps failing, so every code is an instruction. The rule is **"we couldn't"
gets a retry, "we won't" does not.**

| Status | When | Because |
| --- | --- | --- |
| **200** | delivered, or deliberately dropped | an unapproved sender, a body no adapter can parse, a reaction — a retry brings the same thing back |
| **403** | bad signature, unknown channel, deleted channel, a channel belonging to another provider | all four are the same answer on purpose: a different status or message tells a stranger which channel ids exist |
| **413** | body over `maxBody` | |
| **500** | our database or the sink failed | the one failure a retry actually fixes. Every write on this path is idempotent, so asking again is free — answering 200 loses the message with nothing anywhere recording that it existed |
| **503** | a declared credential could not be read | fail closed. `Verify` is never called with `""` |

The handler reads the body **before** looking the channel up. Reading second
makes how quickly a request is refused into an oracle for which channel ids are
real.

## Verify

- **`hmac.Equal("", "")` is true.** Every `Verify` must refuse an empty secret
  explicitly, or a missing credential becomes a forgery oracle: anyone signing
  with `""` is accepted. `Credentials.Get` returning `""` for an absent key is
  convenient and is exactly why this matters.
- **`hmac.Equal`, never `==`.** A string compare returns faster on a wrong
  first byte, and that timing is enough to guess a signature.
- **The signature covers the raw bytes.** Decode the JSON and re-encode it and
  it will never match — key order and whitespace change.
- **Freshness windows are symmetric.** The sender chooses the timestamp they
  sign, so a one-sided check lets them sign an hour into the future and replay
  it all afternoon.
- **A provider declaring no keys cannot be verifying anything**, so
  `NewRegistry` refuses it. That catches the `Verify` that was stubbed during
  development and never finished — the mistake this whole package is shaped
  around.
- **The handler fetches every key `Keys()` names, or refuses.** A declared key
  the store does not hold is a 503, never an empty string handed to `Verify`.

## Validate belongs to Inbound

`Inbound.Validate()` holds the rules the handler depends on: an `ExternalID`,
an `Author.Ref`, a `Session.Ref`, a known `Kind`, and a `SenderRef` on
every mention. Written once, because they are the same rules whoever produced
the message.

It is **not** a `Provider` method. Every implementation would be identical,
which makes it a shared function wearing an interface — and an adapter could
stub it to `return nil`, which is the same bug in a new place. A method on
`Inbound` cannot be overridden by an adapter.

The dividing line: **rules about the type live on the type; rules about the
format live in the adapter.** `ExternalID` must be non-empty whoever produced
the message. `sent_at` must be RFC 3339 because *our JSON* says so, and Slack
sends a float string — so that rule stays in `custom`.

The handler calls `Validate` on everything `Parse` returns, in `resolve`, and
that is the only production call site. Adapters do not call it themselves: a
rule each implementer must remember to apply is documentation, not enforcement.
`providertest` checks it too, but the suite is opt-in and so cannot be the
guarantee — the handler is on the path every message takes.

A message that fails is dropped and logged; the rest of the batch survives, and
its sender is *not* queued for approval. An adapter bug must not be able to
fill a channel's pending list.

## Writing an adapter

Every `Parse` has the same shape: **decode, map, return.** What differs is only
the middle.

Things that are easy to get wrong:

- **`ExternalID` must be unique across the channel, not just the payload.**
  Slack's `ts` repeats across channels — compose `channel:ts`. Getting this
  wrong means the second conversation's message is silently dropped as a
  replay.
- **`Session.Ref` is the whole of session identity, and it is the adapter's to
  choose.** Two threads running in one channel must not share a ref, or the
  agent reads two unrelated arguments as one. A thread's ref includes its
  parent — Slack's `1699999999.0001` is unique only inside one channel.
- **`SessionKind` describes how many people can speak**, not what the provider
  calls the room. Slack's `mpim` looks like a DM and is `group`.
- **Refs are strings and are scoped by the channel.** Telegram sends numbers;
  they are still strings, because nothing does arithmetic on them. `U01AA` in
  one workspace has nothing to do with `U01AA` in another, which is why
  `channel_senders` is keyed on `(channel_id, ref)`.
- **`IsBot` covers two situations.** Another bot is useful context; *our own*
  bot is the assistant's prior turn and also the infinite loop. The runtime
  tells them apart by comparing `Author.Ref` to our bot id.
- **`DisplayName` is attacker-controlled** on every platform adapter — it is
  typed by the person speaking. Clipping and control-character stripping happen
  once, where senders are recorded, because the gateway cannot know which
  adapters are trustworthy and must assume none are.

## A channel has sessions; a session has messages

`Session.Ref` answers "which conversation is this", and the adapter is the only
thing that can answer it. Same ref, same conversation. New ref, new one.

| platform | situation | ref | kind |
| --- | --- | --- | --- |
| Slack | DM with the bot | `D01ABC` | `direct` |
| | a channel | `C01ABC` | `group` |
| | a thread in it | `C01ABC:1699999999.0001` | `thread` |
| | group DM (`mpim`) | `G01ABC` | `group` |
| Telegram | private chat | `4471122` | `direct` |
| | a forum topic | `-1001234567:42` | `thread` |
| WhatsApp | anyone | the phone number | `direct` |
| custom | whatever downstream sends | `ticket-4821` | its choice |

`EnsureSession` is find-or-create and "somebody spoke" in one statement, so a
message never arrives at a session that does not exist and the session's place
in the list is always the last time it was used.

**A provider gives you identity, never episode.** It tells you which room and
which person; it does not tell you where one conversation stopped and the next
began. Only a clock can, and only for the adapters that need it:

- A **Slack thread** ends on its own — the ref stops being used.
- A **Slack DM, a Telegram chat, an untreaded channel, any WhatsApp
  conversation** never end. One ref, forever, which is a relationship rather
  than a conversation.

Three of Slack's four conversation types are in the second group. Threads are
the exception that makes "one session per ref" look sufficient, and they are
what you will test with.

So `sessions` carries `status` and `last_message_at`, and nothing writes them
yet. The unique index is partial — `WHERE status = 'open'` — which today means
exactly one session per conversation and find-or-create is unambiguous. When an
idle sweep lands, the same index lets the next message open a second row for
the same ref, and `EnsureSession` does not change. That is the whole reason the
columns are there before anything uses them.

**A session belongs to a team, not to a channel.** A channel is one way a
session starts; the dashboard and the API are others, and neither has a webhook
behind it. It is also what a workspace or a sandbox will hang off, which is why
it is a row with an id rather than a grouping derived from messages.

`sessions.team_id` duplicates what `channels.team_id` already says whenever
there is a channel. The database keeps them equal rather than trusting every
writer: `channels` has a `UNIQUE (id, team_id)` for a composite foreign key to
reference, so a session naming a team its channel is not in cannot be written.

**`messages` still carries `channel_id`.** Not for convenience — the unique
index that makes retries free is `(channel_id, external_id)`, because a
provider's message id is unique within its channel and not within a session. A
redelivery arriving after a session closed would otherwise be stored twice.
Denormalise when a constraint needs the column, not when a query would find it
handy.

## Attachments are named here and fetched later

`Parse` cannot download, so an attachment it produces is a `Ref` plus what the
provider claimed about the file:

```go
type Attachment struct {
	Ref              string
	ClaimedFilename  string
	ClaimedMediaType string
	ClaimedSize      int64
}
```

Three of those four fields are chosen by whoever sent the message, and the
names say so. The ingest path re-derives the type from the bytes it actually
received and enforces `objects.MaxAttachment` against the bytes it actually
counted — `ClaimedMediaType` and `ClaimedSize` are a hint for logging and an
early cheap refusal, never the value that gets stored.

`Ref` is the exception: it is the only field the handler trusts, because it is
the only one it cannot work without.

**Fetching is a separate, optional interface.**

```go
type Fetcher interface {
	FetchAttachment(
		ctx context.Context, req WebhookRequest, ref string, creds Credentials,
	) ([]byte, error)
}
```

Optional rather than a `Provider` method, for the reason at the bottom of this
file: a provider with no concept of an attachment has nothing to implement, and
a stub returning `nil, nil` is a missing feature that reads as an empty file.
`providertest` checks both directions — refs with no `Fetcher`, and a `Fetcher`
nothing will ever call.

**It takes the request, not only the ref.** A ref alone is enough for Slack,
whose refs are file ids you hand back to its API with a token. It is not enough
for a provider whose bytes arrive inside the delivery, where the ref means
nothing without the body it came in. That is what `custom` exposed and three
downloading adapters would have agreed to get wrong together.

**Fetching happens inline, between resolve and the write.**

```text
Parse -> resolve (drops strangers) -> fetch (network) -> Deliver (write)
```

Both boundaries are load-bearing. *After* resolve, because resolve is what
drops an unapproved sender and a message that failed `Validate` — fetching
before it lets a stranger who was never approved for the channel make us
download and store bytes under a team's key, objects no row will ever name.
*Before* the write, because a download must not hold a row lock open.

It is bounded by `fetchBudget`, two seconds for the whole delivery and one for
a single file. Those numbers come from the ack window rather than from what a
download needs: Slack allows three seconds. Sized this way, inline fetching
cannot blow an ack — and a file that does not fit is dropped with a log line,
which is the signal that the provider needs a queue rather than a bigger
constant. Raising them past the ack window trades a lost file for a lost
message and an endpoint the provider eventually disables.

**A failed fetch is a log line; a failed row is an error.** They look
symmetrical and are not. A file that 404s would 404 on every retry, so the
message is stored without it and the answer is 200. By the time a row is being
written the bytes are already in the bucket, so a failure there has to be a 500
— answering 200 leaves an object nothing names and a message nobody has. The
replay skip is what makes that retry safe.

**A replay writes nothing, and that is not only about the message.** The fetch
step has no memory: a redelivery downloads the same file again and stores it
under a fresh object key. Without the skip, that second key gets a row and the
first object is orphaned — and deleting the message later cascades the row away
and leaves the bytes forever.

**A ref is unique within one delivery.** `FetchAttachment` gets a ref and
nothing else, so two attachments sharing one means the second resolves to the
first one's bytes and is stored under the second one's filename — an ingest
that succeeds and is wrong. The suite asserts uniqueness; adapters that build a
ref with a fallback have to check the *computed* ref, because an id can collide
with another attachment's fallback form.

## The conformance suite

`providertest.Run(t, p, fixtures)` is an adapter's whole test file. It asserts
what the handler assumes and cannot check for itself: six ways of refusing a
forgery, fifteen hostile bodies through `Parse` without a panic, determinism,
that neither `Verify` nor `Parse` mutates the body, and that attachments are
named in a way something can resolve.

Its own tests break a correct adapter one property at a time and require the
suite to notice each break. Because the suite reports through `*testing.T`, the
only way to assert it failed is to run it in a child process and read the exit
status — that is what `PROVIDERTEST_BROKEN` selects. Coverage for those
branches does not merge back into the parent profile, which is why
`providertest` reports below 100% while being fully exercised.

**`custom` ships rather than being a fake.** A test double is written by the
same person, at the same time, with the same misunderstandings as the thing it
validates, so it agrees with the suite by construction. A real second
implementation with users is what proves the abstraction rather than the code.

## Settled: who may read a direct session

**Its participant, plus anyone holding `session:read_all` in the team.** A
`group` or `thread` session stays readable by the whole team — a shared room is
shared — and a `direct` one does not, because on every platform that is a
private message: the person sent it to the agent, not to the room.

The gateway is where the participant is recorded. `Deliver` sets
`sessions.user_id` to the approved sender for a direct session and to nothing
for any other kind, and nothing else writes that column — the API never creates
a session. `EnsureSession` leaves it alone on conflict, so a second approved
sender reaching the same conversation cannot take over somebody else's thread.

Two things that look like details and are the whole rule:

- **A group session naming a participant is refused by the database**, not just
  avoided here. It would read as "this belongs to one person" while several
  people speak in it.
- **Null means nobody, never everybody.** `user_id` is `ON DELETE SET NULL`, so
  deleting a person leaves their direct sessions with no participant. In SQL
  `user_id = $viewer` is null there, and null is not true, so those rows fall
  out of every non-moderator's list. Read the other way — "unset, so
  unrestricted" — it would publish exactly the conversations that were private.

A permission rather than a role, because which role holds it is data in
`rbac.json`. A super admin passes it the way they pass every permission.

## Not here

Reading a session back. `internal/api/sessions` serves that; the gateway only
writes. Outbound replies, and socket transports. Slack Socket Mode and the
Discord gateway are long-lived connections and `Provider` describes a request,
so they need a second interface with a lifecycle.

The queue. Ingest is inline, which is correct for a provider whose bytes arrive
in the delivery and is bounded by the ack window for everyone else. A provider
with remote files and a three-second window will not fit, and the
"attachment budget exhausted" log line is what says so.

When outbound lands it is a `Replier` interface alongside `Fetcher`, not a
method on `Provider` — an adapter for a plain incoming webhook has nowhere to
post back to, and a stub returning nil is indistinguishable from success.
Detected at registration, never with a type assertion at the call site. **A method belongs on `Provider` only when every
implementation must have it and their implementations genuinely differ.**
