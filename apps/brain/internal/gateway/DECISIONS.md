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

**A file the provider holds is downloaded, and the adapter still holds no
client.** `Downloader` builds each request (the first from the ref, each next
from the answer before) and the ingest sends them with the client that dials
public addresses only and follows no redirect, as replies are sent. Telegram
is two requests: `getFile` for a path, then the path under the bot's token.
No error quotes a request's URL, since a provider's carries its token.
Considered: `Fetcher` doing the network itself, which hands every adapter a
client; and one request per file, which Telegram's API does not allow.

It is bounded by `fetchBudget`, two seconds for the whole delivery and one for
a single file. Those numbers come from the ack window rather than from what a
download needs: Slack allows three seconds. Sized this way, inline fetching
cannot blow an ack — and a file that does not fit is dropped with a log line,
which is the signal that the provider needs a queue rather than a bigger
constant. Raising them past the ack window trades a lost file for a lost
message and an endpoint the provider eventually disables.

A provider whose webhook waits for its answer may say so (`Budgeted`):
Telegram gives its files six seconds each and eight in all, since a slow
answer only holds back the bot's next update. Considered: a queue that
fetches after the message is stored, which needs the run to wait for it;
"attachment budget exhausted" in the logs is when that becomes worth it.

**A file not kept is said to the run.** Each is a sentence in
`Delivery.Missed` ("film.mov is video/quicktime, which is not accepted",
"report.pdf could not be fetched"), and the run's input ends with them, so the
agent tells the person rather than answer as if it had seen the file. A
message whose every file was missed is still answered for that reason.

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

## Telegram: a shared secret, rich messages, and getMe

Telegram proves a delivery with the `secret_token` given to `setWebhook`,
echoed in `X-Telegram-Bot-Api-Secret-Token`. It signs neither the body nor a
timestamp, so `providertest` is told so (`SignsNoBody`, and no
`EnforcesFreshness`) rather than weakened for everyone; a repeated update is
dropped by its `ExternalID`, `chat:message_id`, since a message id is unique
only inside its chat. The generated signing secret is the `secret_token`: its
`oawh_` + base64url form is within Telegram's `A-Z a-z 0-9 _ -`.

Replies go out with `sendRichMessage` and `rich_message.markdown`, Bot API
10.1, so an agent's Markdown (headings, lists, tables, code) is sent as
written, up to 32768 characters and 500 blocks a message. Considered:
`sendMessage` with MarkdownV2 or HTML, which needs a converter and fails the
whole message on one unescaped character. Two rewrites run first, outside
code only: an image becomes a link, because Telegram fetches an image's URL to
show it and an injected `![](https://x/?data)` would leak without a click; and
raw HTML is escaped, because an unknown tag refuses the message and no retry
fixes it.

A long answer is split between blocks, never inside a code block unless one
block is itself too big, and then the fence is closed and reopened around each
cut. Considered: one request a reply, cut at the limit. Refused because the
user wants the whole answer; the cost is `parts_sent` and resuming.

A photo is kept at its largest size a bot may download (20 MB, the Bot API's
limit), named `photo-<message_id>.jpg`; a document and audio keep their names;
a caption is the text. A voice note, a video or a sticker names no file. An
album arrives as one update per photo, so the first starts a run and the rest
steer it; merging them would mean holding a message back to wait for the next.

`Parse` receives the credentials, because "addressed to us" in a group needs
the bot's id and username. Considered: answering only private chats. Refused
because groups are where a team's bot is most used. The username comes from
`getMe` when the channel is connected (`Setup`), not from a person, so a typo
cannot silently mute the bot in every group.

The brain calls `setWebhook` itself (`Connector`), with the hook URL, the
secret and `allowed_updates=["message"]`, as Hermes and OpenClaw do at
startup. Considered: printing the command for the person to run, which is
what happens when the brain has no public https address, since Telegram posts
only to https. A refusal or Telegram out of reach removes the channel and its
secret and answers 502 with Telegram's reason: a channel nothing posts to
looks connected and is not. Deleting a channel calls `deleteWebhook` first,
best effort, so a person's delete is never held up by Telegram.

## A stranger is told, from an outbox of its own

A sender nobody has let in used to hear nothing, and from their side the bot
was broken. Now the first time they ask the bot in a wait, it says they are
waiting, and when they are let in, it says so and asks them to send their
message again (it was never stored). Telegram only: `Notifier` is optional
beside `Replier`, and `custom` is left alone because its receivers already
parse a reply's shape.

A notice is not a reply. A reply hangs off a run, a session and a stored
message, and a stranger has none of the three, so `notices` is its own table,
addressed by the provider's ids (conversation, message) kept on the pending
row. Never what they wrote. One sender drains both tables with one attempt
path, so lease, retry and give-up exist once.

Once per wait is `pending_senders.notified_at`, set by the statement that
writes the notice, not `seen_count = 1`: in a group the first message may not
name the bot, and the one that does would never be told. Two messages at once
make one notice. "You're in" is written by the approving statement and only
when "waiting" went, so nobody is welcomed who was never told to wait.

The brain owns the words and the adapter names the sender: in a group by a
link to their account, so nobody else takes it as theirs; the name is the
sender's own text, so it is escaped. A notice that cannot be written is
logged and the stranger stays queued: the queue is what they came for.

## Only a stranger who asked is queued; privacy mode is said at connect

`ResolveSender` queues an unknown sender only when the message is addressed
to the bot, by the same rule that decides whether a message is answered. A
Telegram bot with privacy mode off is sent everything said in a group, and
queueing everyone who spoke would fill the 50-place queue with people who
never asked for anything.

A new Telegram bot has privacy mode on, and is then sent in a group only
commands and replies to it, so a plain mention never arrives; this was found
on staging, not in Telegram's reference, which does not list mentions either
way. `getMe` reports `can_read_all_group_messages`, so `Setup.Complete`
returns warnings beside the credentials and the create response carries them.
They are said once, at connect, and not stored: the setting can change in
BotFather at any time, and a stored one would keep warning after it was
fixed. Considered: refusing such a bot. Refused because private chats,
commands and replies work, and some teams want exactly that.

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
