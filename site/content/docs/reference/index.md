---
title: Reference
---

What exists, named exhaustively. Every page here is a list rather than an
argument; the reasoning lives in [Concepts](../concepts/index.md) and the narrative in
the [Platform](../platform/index.md) and [Agent SDK](../agent-sdk/index.md) sections.

<div class="grid cards" markdown>

  - **[oa, the CLI](cli.md)**: Every command and subcommand, and the flags that apply to all of them.
  - **[HTTP API](http-api.md)**: Every endpoint the brain serves, and how a caller authenticates.
  - **[Configuration](../platform/configuration.md)**: Every environment variable, its default, and whether it is used yet.
  - **[Go API](https://pkg.go.dev/github.com/LaplacianAI/openarity/sdk/agent)**: sdk/agent on pkg.go.dev — generated from the source, so it is never stale.

</div>

Three of these four are generated or checked against the code: the Go API is
built from the source, the endpoint list is compared against the routes the
service registers, and the command tree comes out of `oa --help`. The
configuration table is the one a person maintains, which is the one to distrust
first.
