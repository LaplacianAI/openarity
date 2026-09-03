---
title: Agent SDK
weight: 5
next: /docs/agent-sdk/install
---

`sdk/agent` is the agent loop as a Go library. It receives a fully resolved spec
— a model, a pattern, a system prompt, tools and skills — runs the loop, and
returns the conversation it produced.

It decides nothing. It does not choose a model, authorise a tool, or persist a
message; every one of those is handed in per run. So it drops into any Go
program, and it is a separate module precisely so that it cannot grow opinions
about the platform it was written for.

```sh
go get github.com/LaplacianAI/openarity/sdk/agent
```

{{< cards >}}
  {{< card link="install" title="Install and first agent" subtitle="A whole working agent in about forty lines." >}}
  {{< card link="patterns" title="Patterns" subtitle="ReAct, plan-then-act, ReWOO and reflection — what each costs and when it wins." >}}
  {{< card link="custom-patterns" title="Writing a pattern" subtitle="The extension point that matters: your rules, in your code, keeping the shipped name." >}}
  {{< card link="mcp" title="MCP servers as tools" subtitle="Connect a server, get []agent.Tool. The loop never learns MCP exists." >}}
  {{< card link="tools-and-skills" title="Tools and skills" subtitle="Why sixty skills cost one entry in the tool list." >}}
{{< /cards >}}

## Why it is separate

Which tools and skills a run may see is an authorisation decision. Keeping the
loop in its own module means that decision cannot leak into it — the compiler
refuses the import rather than a reviewer noticing.

The same property is what makes it useful to you: nothing else in the
repository comes with it. One direct dependency, no database, no server, no
configuration file.

{{< callout type="info" >}}
Reference documentation is generated from the source on
[pkg.go.dev](https://pkg.go.dev/github.com/LaplacianAI/openarity/sdk/agent).
These pages are the narrative half.
{{< /callout >}}
