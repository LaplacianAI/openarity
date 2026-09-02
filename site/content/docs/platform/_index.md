---
title: Platform
weight: 4
next: /docs/platform/quick-start
---

The `brain` service, `oa` the CLI, and the inbound gateway they share.

{{< cards >}}
  {{< card link="quick-start" title="Quick start" subtitle="A brain against a local Postgres, in about five commands." >}}
  {{< card link="configuration" title="Configuration" subtitle="Every environment variable, its default, and whether it is used yet." >}}
{{< /cards >}}

## What runs today

The brain is production-shaped, but it does not yet do anything an agent
platform does:

- Two HTTP listeners, one for the API and one for webhooks, with full timeouts
  and graceful shutdown
- Liveness and readiness probes; readiness checks Postgres and returns 503 when
  it cannot reach it
- Environment-only configuration, validated at startup, with credentials
  redacted whenever config is printed
- Versioned migrations behind an advisory lock, and type-safe queries generated
  from SQL by sqlc
- Authentication against an OIDC provider, or a shared token for development
- Role-based authorisation, where roles and their permissions are rows
- A teams API, and an inbound gateway that verifies a channel's webhook against
  that channel's own signing secret

## What does not exist yet

The graph, the planner, the dashboard, and outbound replies — the brain can
hold a conversation's messages but cannot yet answer one. The agent loop runs
end to end against a real gateway, but nothing in the brain calls it, so no
message reaches a model.

Slack, Discord and Telegram adapters are not written. The seam they plug into
is, and a generic webhook adapter works today.
