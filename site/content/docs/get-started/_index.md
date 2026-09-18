---
title: Get started
weight: 1
aliases:
  - /docs/install/
next: /docs/get-started/docker
---

There are three ways in, and which one you want depends on what you are about
to do.

{{< cards >}}
  {{< card link="docker" title="Run the whole stack" icon="server" subtitle="One command: Postgres, a secret store, an identity provider, the brain and the dashboard." >}}
  {{< card link="/docs/platform/quick-start" title="Run the brain from source" icon="download" subtitle="A brain against a Postgres you already have, in about five commands. Needs Go." >}}
  {{< card link="/docs/agent-sdk/install" title="Use the SDK on its own" icon="code" subtitle="go get one module and write an agent. No brain, no database, no configuration file." >}}
{{< /cards >}}

## Which one

**The compose stack** is what a deployment looks like: a real identity provider,
a secret store that survives a restart, and no development token anywhere. Use
it to see the product.

**The brain from source** is the loop to write code in. It needs no identity
provider and no secret store — a shared token stands in for both — so
`go run ./cmd/brain` tests the code you just changed rather than what a
deployment does.

**The SDK on its own** shares nothing with either. It is a separate Go module
with one dependency, no database and no server, so it drops into a project that
has no interest in the rest of Openarity.

{{< callout type="warning" >}}
Nothing is released. There is no image to pull that is not built from this
repository, no signed installer, and no stable API. Every page here says what
is built and what is not.
{{< /callout >}}
