---
title: Install
weight: 4
next: /docs/install/docker
---

Getting Openarity running, for someone who has just cloned it.

{{< cards >}}
  {{< card link="docker" title="Docker" icon="server" subtitle="One command: Postgres, a secret store, an identity provider, the brain and the dashboard." >}}
{{< /cards >}}

There is a second way, and it is the one to use while writing code: run the
brain directly against a Postgres you already have. That is the
[platform quick start](/docs/platform/quick-start), and it needs no identity
provider and no secret store — a shared token stands in for both.

The difference is what you are testing. `go run ./cmd/brain` tests the code you
just changed. The compose stack tests what a deployment does: a real identity
provider, a secret store that survives a restart, and no development token
anywhere.

{{< callout type="warning" >}}
Nothing is released. There is no image to pull that is not built from this
repository, no signed installer, and no stable API. Every page here says what
is built and what is not.
{{< /callout >}}
