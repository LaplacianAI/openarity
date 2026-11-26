# internal/gateway

Read [apps/brain/AGENTS.md](../../AGENTS.md) first; this file adds what is particular to the gateway.

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
  handler.go        the request path: route, verify, parse, then the Receiver
  receive.go        Receiver: resolve each sender, keep the files, deliver; a webhook's and a socket's alike
  senders.go        ResolveSender, and the display-name cleaning
  sink.go           where a delivery goes; inbox.go is the stand-in
  answer.go         whether a stored message starts a run, steers one (giving up its sender's own requests), or is left
  approve.go        an answer given in the chat (typed, a tap, a vote): matched to its request and decided
  send.go           the outbox's replies and notices: claim, build through the provider, post, mark
  job.go            the reply queue, the kick a finished run sends, the sweep
  providertest/     the conformance suite every adapter runs
  custom/           the generic adapter, and the reference implementation
  markdown/         an answer cut into messages a platform takes, and rewritten outside code
  slack/            a Slack app: v0 signatures in, markdown_text out in threads, auth.test at setup
  telegram/         a Telegram bot: secret_token in, rich messages out, getMe at setup, files by getFile
  whatsapp/         a number linked as a device: pure Map and Reply; the connection is internal/whatsapp's
  <provider>/       one package per platform
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

No network, no clock, no randomness, no database. It is handed the channel's
`Credentials` as values, as `Verify` is: Telegram needs its bot's id and
username to tell a group message addressed to it from one to another bot.
This is not a style preference — it is the property that lets `providertest` throw hostile bytes at
an adapter and assert it never dials out.

Go cannot express it in a type, so the suite enforces it by calling `Parse`
twice with the same bytes and requiring the same answer. An adapter reading a
counter or the clock fails in its own test, naming itself, rather than
surfacing as an intermittent handler failure three tasks away.

Consequences that follow from purity:

- **`Attachment` holds a `Ref`, not bytes.** Fetching happens afterwards,
  through the optional `Fetcher` (bytes in the delivery) or `Downloader`
  (requests the gateway sends). See [DECISIONS.md](DECISIONS.md).
- **`Enrichment` is where deferred work goes.** It is defined by a constraint,
  not a category: everything `Parse` was forbidden from doing. Substitutions
  that need `channel_senders`, a place name that needs geocoding.
- **`Text` is never rewritten in place.** It stays as the provider sent it, so
  what arrived is still auditable after rendering.

## What each status code means

A provider retries on any non-2xx and eventually disables an endpoint that
keeps failing, so every code is an instruction. The rule is **"we couldn't"
gets a retry, "we won't" does not.**

| Status | When | Because |
| --- | --- | --- |
| **200** | delivered, or deliberately dropped | an unapproved sender, a body no adapter can parse, a reaction — a retry brings the same thing back |
| **403** | bad signature, unknown channel, deleted channel, a channel whose team is deleted, a channel belonging to another provider | all five are the same answer on purpose: a different status or message tells a stranger which channel ids exist. A team deleted after the channel was read is refused inside `Deliver`, which tombstones the objects already fetched for it in the same transaction |
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

## The dashboard channel

Every team has one channel with provider `dashboard`, made with the team. It
has no adapter, so its hook path is a 404 like any unknown provider's: what a
person types on the dashboard reaches the inbox from the API, through
`Inbox.DeliverOne`, which delivers as `Deliver` does and says what became of
the message. Its runs queue no reply; the session's stream is the answer.

A dashboard conversation is the hub's unless it started as the builder's
(`Session.Builder`, set only when it begins): then its messages start the
builder, on the team's model. Every other agent is reached through the hub.

## Not here

Reading a session back, or an attachment. `internal/api/sessions` serves both;
the gateway only writes. The promise this package makes by sniffing — that the
recorded `media_type` is what a reader is given — is kept there, by serving it
exactly and under `nosniff`. Holding a socket open: a provider that is a
`Socket` (WhatsApp's linked device) keeps its adapter pure and leaves the
connection to the process that holds it, outside this package.

The queue. Ingest is inline, which is correct for a provider whose bytes arrive
in the delivery and is bounded by the ack window for everyone else. A provider
with remote files and a three-second window will not fit, and the
"attachment budget exhausted" log line is what says so.

## Replies

`Replier` sits beside `Provider`, not on it: an adapter for a plain incoming
webhook has nowhere to post back to, and a stub returning nil is
indistinguishable from success. `NewRegistry` records which providers have it;
callers ask `reg.Replier(name)`. **A method belongs on `Provider` only when
every implementation must have it and their implementations genuinely differ.**

`Reply` is pure, like `Parse`: it builds the `OutboundRequest`s from values
(the channel, the reply, the credentials, the time) and never sends them. A
reply too long for one message is several requests, sent in order; the same
inputs must split the same way, and `replies.parts_sent` records each part as
it goes, so a retry resumes at the first part not sent rather than sending
one twice. The sender posts each with the one client that refuses private addresses at dial time and
follows no redirect. So an adapter still holds no HTTP client, and:

- **An error from `Reply` is final.** The same inputs give the same answer, so
  the sender gives the reply up at once. A failed POST, a non-2xx or an
  unreadable secret is retried: 1, 5 and 30 minutes, then hourly, six
  attempts in all.
- **Replies are sent in order within a conversation.** The claim takes a reply
  only when nothing older in its session is pending, so one waiting to retry
  holds back its own conversation and nothing else.
- **A claim is a lease, not a held lock.** It pushes `next_attempt_at` out two
  minutes and counts the attempt; a mark matches on that count, so a sender
  whose lease ran out cannot overwrite a newer attempt.
- **Delivery is at least once.** A POST that succeeded before the worker died
  is sent again; every reply carries an id for the receiver to drop a repeat
  (`replyID`). The run's answer, or its apology, carries the run's id, an
  event `<run id>:<seq>`, and anything else, such as the notice that an
  answer in the chat was taken, `<run id>:<kind>:<reply id>`: under the run's
  id it would be taken for the answer, and the answer dropped as its repeat.
- **A finished channel run kicks the queue**, so a reply goes out at once; the
  sweep every minute is for retries and lost kicks.

A provider may also be a `Notifier`: it tells a sender nobody has let in that
they are waiting (once per wait) and, when they are let in, that they are in.
A notice is built like a reply and sent by the same sender, from its own
`notices` outbox, since a stranger has no run or session to hang it on.
Telegram is one; `custom` is not. A channel's `notify_strangers` switches it.

A channel's `reply_mode` is `final`, its run's answer alone, or `events`: each
step a person would follow first, a reply of kind `event` with its own id
(`<run id>:<seq>`), tried twice and quoting nothing. Only an `EventReplier`
takes the mode; `telegram` and `custom` are. The runtime decides which steps
are told and in what words, and writes each after its run event.

A provider that says no inside a 2xx, as Slack's `"ok": false` does, sets
`Accepted` on the `OutboundRequest` it builds: the poster and the download
hand it the answer, and its error is the request's, retried unless it wraps
`ErrUnsendable`. A `SecretIssuer` is a provider whose platform issues the
signing secret (Slack's app settings), so a channel is refused without one
rather than given one the brain made.

A provider may be a `FileReplier`: it sends a file a run hands its person,
a reply of kind `file` with `Outbound.File` set, and only on its channels is a
run given `send_file`. It declares `MaxFile`, the most its platform takes,
and `NewRegistry` refuses one that replies with files and names none. Its
`file.go` builds the file message around the file, as `Body ‖ file ‖
Stream.Tail` with the file's size, so `Reply` stays pure; the sender opens the
file only as the request is posted, decrypting a chunk at a time, one file at
a time (Telegram's `sendDocument`, Slack's upload, WhatsApp's document). A
`WholeFiles` provider is handed the bytes instead: `custom`, whose inline
base64 is signed with the body. A file gone since is given up;
a store that fails is retried. A `FileUploader` is a `FileReplier` whose
platform takes a file in steps, each built from the answers before it:
`FileRequest` is pure like `Reply`, and the sender runs the steps as one part,
starting again from the first on a retry. Slack is one: upload address, bytes,
share.

A channel takes a `reply_url` only when its provider is a `URLReplier`, which
posts to an address the channel names. `custom` is; `telegram` replies into
the chat itself, so it can reply and takes no URL.

**`Setup` completes credentials when a channel is connected.** A provider that
needs more than a person can type (Telegram's bot id and username, from
`getMe`) declares `Supplied()` keys, builds the request and reads the answer;
the channels API sends it with the reply client. Like `Reply`, it holds no
client. **`Connector`** points the provider at a channel (Telegram's
`setWebhook`) when it is created and lets it go (`deleteWebhook`) when it is
deleted; a refusal on create takes the channel away again, since nothing would
ever reach it, while a refusal on delete is logged and the channel goes anyway.
Every key a channel holds is written as one secret with `PutAll`:
OpenBao's KV write replaces the whole secret, so a `Put` per key keeps only the
last.

**A `Socket` holds a connection instead of taking webhooks.** It declares no
routes (`NewRegistry` refuses one that does, since a mounted route would be a
webhook nothing verifies) and need declare no keys. Its messages reach the
gateway from the process holding the connection (`brain whatsapp`, in
`internal/whatsapp`) through the same `Receiver` a webhook's do, and its
adapter stays pure: WhatsApp's maps a message to `Inbound` and builds
a reply's parts as `OutboundRequest`s addressed `whatsapp:send`. The `Sender`
posts through a `Poster`: the worker's is HTTP and claims every channel but a
socket's (`Scope{}`); the holder's sends through its socket and claims only
the channels it holds (`Scope{Held}`), replies and notices alike. A message
marked `Historical` (sent by the platform when the number was linked) is
stored and never answered.

**Where a channel answers is one rule.** `addressed` decides, from the
channel's `answers_in` (`everywhere`, `groups` or `direct`; empty is
everywhere), whether a message asks anything: whether it starts a run, steers
one, queues a stranger and tells them so. Outside it a message from someone
let in is stored and does nothing else. It is how a WhatsApp number that is
also a person's own answers in their groups and leaves their direct chats
alone. The `Receiver` carries the scope from the channel row to
`ResolveSender` and to the inbox, and the WhatsApp role takes it, with
`notify_strangers`, from each lease. One message is addressed whatever
`answers_in` says: `ToSelf`, the account's owner writing to the account itself
(WhatsApp's "Message yourself"), since nobody else is in that conversation. It
still needs the channel's trigger word when there is one, so notes to self
stay notes; the adapter then drops one without it. The adapter marks it, by
the owner's number; every other message of the owner's without the word is
dropped, and so is one the holder sent, which it names before sending so its
echo is known.

**A trigger word replaces mentions.** A channel's `trigger_word` reaches the
rule as `Answering{Where, Trigger}`. With one, inside `answers_in`, a message
asks only when `HasWord` finds the word in it (any case, a letter or digit on
neither side), in a direct chat and a group alike; mentions no longer count.
WhatsApp's adapter also keeps the owner's own message that holds the word, by
the owner's number, in the chat it was typed in: a direct chat's is the other
person's conversation, by `RecipientAlt` where the chat is a LID. Only a
provider that holds a connection takes a word (`can_trigger_word`).

`custom` signs a reply under `reply:v1:` rather than the `v1:` of an incoming
message, so a captured reply posted to the webhook does not verify as a
message — the team would be answering itself.

## Decisions

The decisions worth not relitigating, each with its reason, are in [DECISIONS.md](DECISIONS.md). Read it before changing a design, a boundary, or anything a convention above protects.
Topics: the result of a delivery (`Result{}`), `Validate` on `Inbound`, sessions and messages, attachments fetched later, who may read a direct session, why a reply is built here and sent there, Telegram, Slack, WhatsApp.
