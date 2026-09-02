---
title: Tools and skills
weight: 4
---

## Tools

A `Tool` carries its own implementation:

```go
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Invoke      func(ctx context.Context, args json.RawMessage) (string, error)
}
```

`Invoke` is a closure you build, so an MCP server, a credential, a database
handle and a network call all stay on your side. The library marshals the
arguments the model produced, calls the function, and puts what it returns back
in the conversation as a tool message. It never learns what the tool talks to.

A tool that returns an error is not fatal. The error text becomes the tool
result, the model sees it, and the run continues — a model that can read
"repository not found" usually recovers, and a run that dies on the first bad
argument cannot.

## Skills

A skill is a name, a description, and a body that is only fetched if asked for:

```go
agent.Skill{
	Name:        "commit-style",
	Description: "How commits are written here. Use before writing one.",
	Body: func(ctx context.Context) (string, error) {
		return os.ReadFile("skills/commit-style.md")
	},
}
```

Descriptions go into the system prompt as a listing. Bodies do not. When the
model wants one it calls the `Skill` tool with the name, and only then does the
body run — which means it can be a file read, a database query or an HTTP call,
paid for only when used.

This is [progressive disclosure](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills),
the way Claude Code loads skills, and the combined description is truncated at
1,536 characters for the same reason it is there.

### Why one tool and not one per skill

Sixty skills cost **one** entry in the tool list.

The tool array is the front of the cached prefix — a provider caches
`tools → system → messages` in that order, so a difference in the tools
invalidates everything after it. Declaring a tool per skill would give every
caller a different tool array, and none of them would share a cache entry. With
one `Skill` tool the array is byte-identical across callers, and only the
listing differs, one block later.

{{< callout type="info" >}}
Two cache breakpoints is the design, not a compromise: the tool list is shared
by everyone, the skill listing is shared by everyone offered the same skills.
{{< /callout >}}

### What the loop guarantees

- A skill loaded twice returns a note the second time rather than the body
  again, so a confused model cannot spend the same tokens repeatedly
- A body that failed is **not** marked loaded, so it can be retried
- Two skills with the same name, or one with no name, is refused when the run
  starts rather than confusing the model later
- A tool already called `Skill` is refused for the same reason
