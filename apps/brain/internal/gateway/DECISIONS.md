# internal/gateway — decisions worth not relitigating

Read on demand from [AGENTS.md](AGENTS.md): before changing the gateway's design, a boundary, or a rule there.

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

So `sessions` carries `status` and `last_message_at`. Every message bumps
`last_message_at`, and the lists order by it. `status` is still only `open`,
though the lists already filter on it. The unique index is partial — `WHERE status = 'open'` — which today means
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

Optional rather than a `Provider` method, for the reason under "Not here" in
[AGENTS.md](AGENTS.md): a provider with no concept of an attachment has nothing to implement, and
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

## A reply is built by the adapter and sent by the gateway

Considered: `Reply(ctx, ch, out, creds) error`, the adapter posting with an
HTTP client it was given. Refused because it hands every adapter — code
written against someone else's documentation, often by whoever wanted that
platform — a client that can reach the network, which is the capability this
package exists to withhold. The pure form keeps the address check, the
no-redirect rule and the error text that reaches `last_error` in one place,
and makes `Reply` testable the way `Parse` is.

What it costs: a provider that answers 200 with an error in the body (Slack's
`"ok": false`) needs one more optional method to read the response. Nothing
needs it yet, so it does not exist.

Ordering is a claim condition rather than a worker per conversation. The
tombstone sweep's rule — no ordering, because deletes commute — does not hold
for replies, and a `NOT EXISTS` over the session's older pending replies gives
the order without a partition scheme to run.

## Settled: who may read a direct session

**Its participant, plus anyone holding `session:read_all` in the team.** A
`group` or `thread` session stays readable by the whole team — a shared room is
shared — and a `direct` one does not, because on every platform that is a
private message: the person sent it to the agent, not to the room.

The gateway is where the participant is recorded. `Deliver` sets
`sessions.user_id` to the approved sender for a direct session and to nothing
for any other kind. Only one other path writes that column:
`CreateDirectSession`, which `POST /teams/{id}/runs` uses when it is given no
session, and which owns the new direct session by the caller. `EnsureSession` leaves it alone on conflict, so a second approved
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
