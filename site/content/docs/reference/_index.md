---
title: Reference
weight: 6
next: /docs/reference/cli
---

What exists, named exhaustively. Every page here is a list rather than an
argument; the reasoning lives in [Concepts](/docs/concepts) and the narrative in
the [Platform](/docs/platform) and [Agent SDK](/docs/agent-sdk) sections.

{{< cards >}}
  {{< card link="cli" title="oa, the CLI" icon="template" subtitle="Every command and subcommand, and the flags that apply to all of them." >}}
  {{< card link="http-api" title="HTTP API" icon="server" subtitle="Every endpoint the brain serves, and how a caller authenticates." >}}
  {{< card link="/docs/platform/configuration" title="Configuration" icon="download" subtitle="Every environment variable, its default, and whether it is used yet." >}}
  {{< card link="https://pkg.go.dev/github.com/LaplacianAI/openarity/sdk/agent" title="Go API" icon="code" subtitle="sdk/agent on pkg.go.dev — generated from the source, so it is never stale." >}}
{{< /cards >}}

Three of these four are generated or checked against the code: the Go API is
built from the source, the endpoint list is compared against the routes the
service registers, and the command tree comes out of `oa --help`. The
configuration table is the one a person maintains, which is the one to distrust
first.
