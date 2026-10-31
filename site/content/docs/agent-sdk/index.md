---
title: Agent SDK
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

<div class="grid cards" markdown>

  - **[Install and first agent](install.md)**: A whole working agent in about forty lines.
  - **[Patterns](patterns.md)**: ReAct, plan-then-act, ReWOO and reflection — what each costs and when it wins.
  - **[Writing a pattern](custom-patterns.md)**: The extension point that matters: your rules, in your code, keeping the shipped name.
  - **[MCP servers as tools](mcp.md)**: Connect a server, get []agent.Tool. The loop never learns MCP exists.
  - **[Tools and skills](tools-and-skills.md)**: Why sixty skills cost one entry in the tool list.
  - **[Steering a run](steering.md)**: Saying something to an agent that has already started, and where those words land.
  - **[Structured output](structured-output.md)**: A schema the answer has to fit, and a cheaper model to fit it.
  - **[Sessions](sessions.md)**: Steering a run no process is holding, and resuming one whose process has gone.
  - **[Examples](examples.md)**: Eleven runnable agents, each one a single file. They need no key.

</div>

## Why it is separate

Which tools and skills a run may see is an authorisation decision. Keeping the
loop in its own module means that decision cannot leak into it — the compiler
refuses the import rather than a reviewer noticing.

The same property is what makes it useful to you: nothing else in the
repository comes with it. One direct dependency, no database, no server, no
configuration file.

!!! info

    Reference documentation is generated from the source on
    [pkg.go.dev](https://pkg.go.dev/github.com/LaplacianAI/openarity/sdk/agent).
    These pages are the narrative half.
