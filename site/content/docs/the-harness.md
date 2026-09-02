---
title: The harness
weight: 2
---

A model with tools is a demo. What makes it a system anyone will run against
their own data is the harness around it: who the caller is, what this run may
touch, what it spent, what happened, and what is left behind when someone is
deleted.

The 2026 enterprise checklist for agents has converged on roughly six things.
This page maps each to what Openarity does today, and says plainly where the
answer is *nothing yet*.

## Identity, and roles that are rows

A caller authenticates against an OIDC provider, or a shared token for
development. The service **refuses to start** with neither configured, rather
than serving an open API — the most common way a pilot leaks is a default that
was convenient.

A verified caller becomes a user row on first sight, so nobody is
pre-provisioned and no directory sync has to run before anyone can be
authorised.

Roles and their permissions are rows in Postgres, not constants in a binary.
Adding a role is a migration; changing what a role may do is an `UPDATE`, not a
release. `internal/authz` names five scopes — `authenticated`, `member`,
`team`, `any_team`, `super_admin` — and every route declares which one it needs
along with the permission it consumes.

## Action boundaries, decided outside the loop

The checklist asks for an explicit allowlist of tools each agent may call. In
Openarity that is not a list a deployment maintains; it is what the brain
resolves per run and hands to the loop.

This is enforced by the compiler rather than by care. `sdk/agent` is a separate
Go module, so it **cannot import** the brain's internals — a pattern has no way
to reach a store, a grant, or a credential. A CI check goes further and fails
if the core packages link a provider SDK at all, because one convenient import
would quietly make the model interface decorative.

The consequence is the shape described in [Why a graph](../why-a-graph): which
tools and skills a run may see is an authorisation decision, made where the
authority lives, and the loop only ever receives the answer.

## What a run spends

Usage is counted at the model client, not by the pattern. A pattern written
outside the module gets the right number without knowing to try, and a run that
failed half way still reports what it spent — which is the case that matters,
because a run that failed expensively is the one you want to see.

Every model call emits a `UsageEvent` as it happens, so a caller watching the
event channel has the cost before the run ends rather than after.

## Secrets, and data nobody can read afterwards

Credentials live in a secret store — OpenBao today, behind an interface — never
in the database and never in configuration that gets printed. Configuration is
environment-only, validated at startup, and redacted whenever it is logged.

Attachments are encrypted with a **per-team data key**. That is not decoration:
it is what makes deletion possible. Postgres has no transaction that spans an
object store and a vault, so deleting a team cannot atomically remove its
bytes. Each deletion records what it owes instead, and `brain reap` settles it
— destroying the team's key *first*, which makes every one of its attachments
unreadable immediately, before a single object has been deleted.

`reap` is idempotent, safe to run twice at once, and exits non-zero when an
erasure has been outstanding for a day. `brain worker` runs it on a schedule and
replays the ticks it missed while it was down.

{{< callout type="warning" >}}
A deployment that never runs the worker never erases anything outside Postgres.
The rows go; the bytes stay.
{{< /callout >}}

## What is not built

Being honest about this is the point of the page.

**There is no audit trail.** Requests are logged with structured fields and the
caller's identity, which is not the same thing. The checklist asks for every
context retrieval and tool invocation recorded against an agent identity, and
that record does not exist yet.

**There is no sandbox.** `code` is a declared pattern name that the runner
refuses, because running model-written code needs process confinement —
Landlock or bubblewrap on Linux, Seatbelt on macOS — and none of it is written.
The design is same-world confinement rather than containers, which is
achievable in-process and needs no privilege the service does not already have.

**There are no approval gates.** Nothing pauses a run for a human before a
high-stakes call. The event stream is the seam this would sit on.

**There is no graph.** The store is Postgres; the graph database is configured
and unused, and marked as such.

## Why the boundaries are structural

Every one of the guarantees above is held in place by something that fails
loudly, not by a convention:

| Guarantee | What holds it |
| ------------------------------------ | ------------------------------------------ |
| The loop cannot authorise            | separate Go module; the import will not compile |
| The loop cannot reach a provider     | a CI check that fails on the linked package |
| Usage cannot be under-reported       | counted at the client, not by the pattern   |
| A deleted team's data is unreadable  | its key is destroyed before its bytes       |
| An open API cannot ship by accident  | refusal to start without authentication     |

A rule a reviewer has to remember is a rule that survives until the first
deadline. These do not depend on anyone remembering.
