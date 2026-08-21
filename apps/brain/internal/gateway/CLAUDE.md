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
  message.go        Inbound, Result, Validate — the normalised contract
  provider.go       Provider, WebhookRequest, Credentials, Registry
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
line there plus a package that passes `providertest`.

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
  through the optional `Fetcher` interface.
- **`Enrichment` is where deferred work goes.** It is defined by a constraint,
  not a category: everything `Parse` was forbidden from doing. Substitutions
  that need `channel_senders`, a place name that needs geocoding.
- **`Text` is never rewritten in place.** It stays as the provider sent it, so
  what arrived is still auditable after rendering.

## Result{} is "nothing to do"

Most of what a busy webhook receives is not a message — a reaction, an edit,
someone joining a room. The zero `Result` is the correct answer, not an error.

Only two things are not a 200: a **403** for a signature that does not verify,
and a **500** when the secret store is unreachable, because that is *unknown*
rather than *denied*. Everything else — junk body, unknown sender, replay —
answers 200. Non-200 makes providers retry and eventually disable the endpoint,
and none of those are conditions a retry would fix.

`Result.Reply` exists for one reason: Slack refuses to save a webhook URL until
the endpoint echoes its `url_verification` challenge. The adapter *returns* the
bytes and the handler writes them, so the adapter still cannot set a status,
stream, or hijack the connection. When one provider needs an escape hatch,
widen the return value, not the capability.

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

## Validate belongs to Inbound

`Inbound.Validate()` holds the rules the handler depends on: an `ExternalID`,
an `Author.Ref`, a `Conversation.Ref`, a known `Kind`, and a `SenderRef` on
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

The handler calls `Validate` on everything `Parse` returns. Adapters do not.

## Writing an adapter

Every `Parse` has the same shape: **decode, map, return.** What differs is only
the middle.

Things that are easy to get wrong:

- **`ExternalID` must be unique across the channel, not just the payload.**
  Slack's `ts` repeats across channels — compose `channel:ts`. Getting this
  wrong means the second conversation's message is silently dropped as a
  replay.
- **`Conversation.Ref` is the thread, not the channel, when there is a thread.**
  Two threads running in one channel must not share a session, or the agent
  reads two unrelated arguments as one.
- **`ConversationKind` describes how many people can speak**, not what the
  provider calls the room. Slack's `mpim` looks like a DM and is `group`.
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
and that neither `Verify` nor `Parse` mutates the body.

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

Outbound replies, attachment ingest, and socket transports. Slack Socket Mode
and the Discord gateway are long-lived connections and `Provider` describes a
request, so they need a second interface with a lifecycle.

When outbound lands it is a third optional interface alongside `Fetcher`, not a
method on `Provider` — every existing adapter keeps compiling and simply
reports `ok == false`. **A method belongs on `Provider` only when every
implementation must have it and their implementations genuinely differ.**
