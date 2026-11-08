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
  handler.go        the request path: route, verify, parse, resolve, deliver
  senders.go        ResolveSender, and the display-name cleaning
  sink.go           where a delivery goes; inbox.go is the stand-in
  answer.go         whether a stored message starts a run, steers one, or is left
  send.go           the outbox's replies: claim, build through the provider, post, mark
  job.go            the reply queue, the kick a finished run sends, the sweep
  providertest/     the conformance suite every adapter runs
  custom/           the generic adapter, and the reference implementation
  <provider>/       one package per platform; only custom ships today
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
  through the optional `Fetcher` interface. See [DECISIONS.md](DECISIONS.md).
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

## Not here

Reading a session back, or an attachment. `internal/api/sessions` serves both;
the gateway only writes. The promise this package makes by sniffing — that the
recorded `media_type` is what a reader is given — is kept there, by serving it
exactly and under `nosniff`. Socket transports: Slack Socket Mode and the
Discord gateway are long-lived connections and `Provider` describes a request,
so they need a second interface with a lifecycle.

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

`Reply` is pure, like `Parse`: it builds an `OutboundRequest` from values (the
channel, the reply, the credentials, the time) and never sends it. The sender
posts it with the one client that refuses private addresses at dial time and
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
  is sent again; every reply carries the run's id for the receiver to drop a
  repeat.
- **A finished channel run kicks the queue**, so a reply goes out at once; the
  sweep every minute is for retries and lost kicks.

`custom` signs a reply under `reply:v1:` rather than the `v1:` of an incoming
message, so a captured reply posted to the webhook does not verify as a
message — the team would be answering itself.

## Decisions

The decisions worth not relitigating, each with its reason, are in [DECISIONS.md](DECISIONS.md). Read it before changing a design, a boundary, or anything a convention above protects.
Topics: the result of a delivery (`Result{}`), `Validate` on `Inbound`, sessions and messages, attachments fetched later, who may read a direct session, why a reply is built here and sent there.
