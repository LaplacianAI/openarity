# gateway — channels

The gateway is the brain's public inbound edge. It exists so that **nothing
downstream ever knows which channel a message came from**. Every adapter turns
one provider's payload into one common `Message` and hands it on; the
orchestrator, executor and everything else see only the normalised shape.

Design: HLD §Gateway, §"Two adapter shapes, one output", §"Webhook mode wherever
the provider offers one", §"Conversation vs Session". The HLD is the bible — read
it before changing the adapter contract.

## The two adapter shapes

Every channel is one of these, and the difference is how it *receives* traffic —
which is also the difficulty and the deploy topology.

- **`WebhookAdapter`** — `Verify(request, body, secret)` then `Parse(body)`.
  Stateless, no lifecycle, any replica may serve any request. Telegram, Slack,
  WhatsApp. The handler fetches the secret from `SecretStore` per request and
  hands the value in — a refinement of the HLD's `Verify(request, body)`
  sketch, proposed to the other maintainer in the first channel PR.
- **`SessionAdapter`** — `Run(ctx, out)`. Owns a long-lived connection, emits
  messages until the context is cancelled. **One owner per account.** Discord
  only, and later — it needs a StatefulSet (one shard per ordinal). Do not let it
  shape the interface early.

Day one is webhook-only. Build order: **Telegram → Slack → WhatsApp → Discord.**
Telegram first because its verification is the simplest, so it is what pins down
the `WebhookAdapter` interface cleanly.

Both shapes normalise into the *same* `Message`. The interface has to fit both
from the start, but only the webhook shape has an occupant today.

## Rules that are not negotiable

- **Verification is per-adapter, on the raw body — never middleware.** Signature
  and token checks depend on the exact received bytes, and each channel signs
  differently (Telegram: a secret token in a header; Slack/WhatsApp: HMAC over
  the body; Discord: Ed25519). Nothing may read or re-serialise the body before
  the adapter's `Verify`. This is why it cannot be a generic middleware — the
  brain's own `add-middleware` skill says the same thing.
- **Secrets come from `secrets.SecretStore`, never from config or env values.**
  Per the HLD, *even webhook signing secrets are references, not values.* The
  handler fetches by path (`secrets.ChannelPath`, `tenants/<t>/channels/<id>`)
  and hands the value to `Verify`; nothing holds a literal. If you are typing
  a token into `config`, stop.
- **Fail closed.** A secret that cannot be fetched, a signature that does not
  verify, a body that does not parse — all are rejected. Never fall back to an
  empty string or an unauthenticated pass.
- **A new channel is an adapter plus registration, nothing else.** No downstream
  package changes when a channel is added. If adding a channel makes you edit the
  orchestrator, the seam is wrong.
- **No adapter until it has an occupant.** Do not write a Slack adapter while
  building Telegram "to be ready". Mirror the brain's no-empty-packages rule.

## Conversation vs Session

The hard part every channel shares (HLD §"Conversation vs Session"):

- **Conversation** — durable, one per chat (the Telegram chat, the Slack channel).
- **Session** — a bounded working context inside a conversation; carries plan
  state and memory.

An adapter declares whether it supplies a thread id. If it does (Slack, Discord),
one thread = one session. If it does not (Telegram, WhatsApp), the gateway falls
back to an idle timeout or an explicit reset. One model, every channel — no
per-channel special-casing above the adapter.

## What an adapter must do (HLD §Gateway)

1. **Verify** the request against the raw body.
2. **Normalise** the payload into `Message`.
3. **Resolve entities** — provider user id → our user/tenant, identify bots.
   This is a lookup into `api` (which owns identity); the adapter calls it
   through its interface, it does not read those tables.
4. **Map** the conversation to a session (thread id or timeout, above).
5. **Render outbound** — take a common-format response and produce what the
   channel expects.

While `api` and the orchestrator do not exist, the downstream of 2 runs against
a **log-only sink** wired in `cmd/brain`. Step 3 (entity resolution) is
deferred entirely rather than stubbed — a resolver interface with only a
constant behind it would fail open. When `api` exists, the resolver seam takes
a tenant id and returns a struct, not a bare user id. Stub the unbuilt, do not
break a rule.

## Testing a channel — with none of the platform built

Adapters are fully testable today against fakes that satisfy the `contracts` and
`secrets` interfaces. The fakes are test-only and live in this package's
`_test.go` files — they never leak into shared code.

The core test is a real HTTP round-trip through the real handler:

- Build an `httptest` request: body = a real provider payload (a real Telegram
  `Update` JSON), plus the verification header.
- Wire the handler with a fake secret store (or `secrets.Static`) and a
  `fakeSink` that captures the `Message`.
- Assert the produced `Message` is correct, and that the sink got exactly one.

Then **attack it** — these are the tests that matter. "Rejected" means the
sink never sees it; the status code is chosen for the provider's retry loop
(401 auth failure, 503 transient, 200-ack-and-drop for payloads that will
never be accepted — providers retry every non-2xx):

- Wrong token / missing verification header → rejected, sink never called.
- Body mutated after signing → verification fails **on HMAC channels**
  (Slack, WhatsApp). Telegram's secret token authenticates the sender, not
  the bytes — pin that as a documented limitation, don't fake it.
- Malformed / truncated / empty JSON → rejected, no panic.
- Oversized body; duplicate updates delivered twice (dedup is downstream's,
  pinned).
- An update the adapter should ignore (edited message, non-message update,
  bot author).

Follow the brain's test conventions: `httptest.NewRequestWithContext` (never the
context-free constructors — `noctx` bans them), `t.Parallel()`, inject config,
reserve real ephemeral ports rather than hardcoding.

## Conventions inherited from the brain

Everything in `apps/brain/CLAUDE.md` applies here — initialisms keep one case
(`URL`, `ID`), tests mirror source files, take the context and writer as
parameters, the logger is a parameter not a global, log fields not sentences, log
`r.URL.Path` not `r.URL.String()`. Read that file; this one only adds the
channel-specific layer.

- **One file per adapter**, named after the channel: `telegram.go`, `slack.go`,
  `whatsapp.go`, `discord.go`. Its test mirrors it: `telegram_test.go`.
- **No new dependencies for inbound.** A provider `Update` is `encoding/json`
  over the raw body; any outbound call is stdlib `net/http`. The HLD hand-rolls
  the loop — no channel SDKs. The one sanctioned exception to the no-SDK stance
  lives in `secrets` (the Vault SDK), not here.
