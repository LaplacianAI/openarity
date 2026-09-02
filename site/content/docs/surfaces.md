---
title: Surfaces
weight: 3
---

{{< callout type="error" >}}
**None of this is built.** This page is direction, not documentation. Nothing
here has a date, an API or an issue you can subscribe to, and the design is
still moving. What runs today is in [Platform](../platform) and
[Agent SDK](../agent-sdk).
{{< /callout >}}

Openarity is one graph with more than one way in.

The surfaces below are not separate products that happen to share a vendor.
They are views onto the same agents, tools, skills and learnings — which means
the interesting property is not any one of them, but what passes between them.

## The property that matters

A learning earned in one surface is capability in every other.

An agent works out, while fixing a flaky test, that this repository's
integration suite needs the database migrated twice. That is a node. It has
edges: to the tool that ran the migration, to the skill that failed without it,
to the team that owns the repository.

Nothing has to be integrated for that to be available the next time somebody
asks a question in chat about why CI is slow. It is not copied, exported or
synced between products. It is reachable, because it is the same graph.

That is the whole argument for building surfaces on one graph rather than
building one product at a time and connecting them later. Connecting them later
means each pair needs an integration, and n products need n² of them; sharing a
graph means each new surface needs none.

## The surfaces

{{< cards >}}
  {{< card title="Openarity Code" subtitle="Agents on a repository — review, migration, the on-call trail. The surface where a learning is cheapest to earn, because tests say whether it was right." >}}
  {{< card title="Openarity Work" subtitle="Agents in the channels people already use. Slack, Discord, Telegram and webhooks normalise into one message, so a surface is an adapter rather than a product." >}}
  {{< card title="Openarity Knowledge" subtitle="The organisation's own graph, queried directly. Not a second knowledge base — the same nodes the agents traverse, with a person doing the walking." >}}
  {{< card title="Openarity Design" subtitle="Agents on design files and design systems, where the constraint is a system rather than a compiler." >}}
  {{< card title="Openarity Mobile" subtitle="The phone client. An agent run is long, so the useful mobile surface is approval and interruption rather than authoring." >}}
{{< /cards >}}

## What already points this way

Three decisions in the code were made for this and are visible today, which is
the only reason this page is worth writing before the products exist.

**Channels normalise.** A channel is a team's connection to one platform. Its
webhook arrives on its own listener, is verified against that channel's own
signing secret, and becomes the same normalised message whatever sent it. A new
surface that speaks to people is an adapter and a conformance suite, not a new
system.

**The loop is a library.** `sdk/agent` is a separate module with one
dependency, so a surface embeds it rather than calling a service. Openarity
Code can run the loop next to a checkout without a network hop to the brain.

**Capability is data, not deployment.** Tools carry their implementation as a
closure and skills spend their description rather than their body, so what an
agent can do is resolved per run. A surface does not ship with its tools
compiled in; it asks.

## What this page is not

It is not a roadmap. There is no order, no quarter and no commitment, and the
graph that all of it depends on does not exist yet.

The reason to publish it anyway is that it explains the shape of what *is*
built. The module boundary, the normalised message, the per-run capability
resolution — each of those costs something today and pays for itself only if
this is where it goes. Someone reading the code deserves to know that.
