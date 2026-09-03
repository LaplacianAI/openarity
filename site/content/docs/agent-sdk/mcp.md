---
title: MCP servers as tools
weight: 4
---

`tools/mcp` connects to Model Context Protocol servers and hands back
`[]agent.Tool`. Nothing in the loop learns that MCP exists — each tool's
`Invoke` closes over the connection, so a pattern dispatches an MCP call exactly
as it dispatches a closure you wrote.

```go
session, err := mcp.Connect(ctx, mcp.Server{Name: "github", URL: "https://mcp.example.com/mcp"})
if err != nil {
	return err
}
defer session.Close()

spec.Tools = session.Tools()
```

That is the whole integration. It is the same shape Agno and LangGraph use —
connect, list, convert to the framework's own tool type — because it is the only
shape that keeps the protocol out of the agent.

## Both transports

A server is either a command or a URL, never both:

```go
mcp.Server{Name: "github", Command: []string{"npx", "-y", "@modelcontextprotocol/server-github"},
	Env: []string{"GITHUB_TOKEN=" + token}}

mcp.Server{Name: "docs", URL: "https://mcp.example.com/mcp", HTTPClient: authorised}
```

{{< callout type="warning" >}}
**A child process inherits nothing unless you say so.** Go's `exec` gives a
`nil` environment the parent's *entire* environment, so a stdio server you just
launched would see every credential this process holds. `Env` is passed through
verbatim and an unset one means empty, not inherited.
{{< /callout >}}

There is no token field on `Server`. A credential for an HTTP server belongs in
your `http.Client`'s transport — a string on a struct ends up in a log line the
first time somebody prints the config.

## Names are namespaced, because they are not yours

A server named `github` offers `github__search`. You do not control what a
third-party server calls its tools, and two servers both exposing `search` is
the ordinary case rather than the exotic one.

```go
tools, err := mcp.Gather(githubSession, jiraSession)
```

`Gather` refuses a name two servers both claim rather than letting one silently
win — the failure that is hardest to notice, because the agent simply stops
being able to do something it could do yesterday.

`Bare: true` offers the server's own names unchanged. Only reach for it with a
single server whose names you know.

Either way the name is checked against `^[a-zA-Z0-9_-]{1,64}$` at connect. A
name outside that is refused by the *provider*, on the first call, naming
neither the server nor the tool; refusing it here says both.

## What happens to a failing tool

A tool that reports failure is not the run failing. MCP has `IsError` on the
result precisely so the model can see what went wrong, and this package puts
that text back into the conversation — the same treatment any other tool error
gets.

A transport failure is different and is returned as a real error: it will fail
again next step and the model cannot fix it.

Content that is not text — an image, an embedded resource — is counted rather
than dropped, so a tool that answers with a screenshot produces
`[1 non-text content block(s) omitted]` instead of an empty string. An empty
string reads to a model as a tool that did nothing, and it calls it again.

## The tool list is read once

At connect, not per call. MCP servers may notify that their tools changed;
honouring that mid-run changes what the agent already decided to do, and for
[ReWOO](../patterns#rewoo) it would change the ground under a plan that is
already fixed. Reconnect between runs instead.

Pagination is followed to the end. A server with more tools than fit in one
response is ordinary, and stopping at the first page loses the rest silently.

## How many tools is too many

Enough that it is the reason this project exists. Measured tool selection
[collapses as the pool grows](../../why-a-graph); a few ordinary servers put
tens of thousands of tokens of schema in front of the model before anyone has
said anything.

So `Session.Tools()` gives you everything the server offered, and choosing is
deliberately yours. Filter before it reaches `Spec.Tools`:

```go
var kept []agent.Tool
for _, t := range session.Tools() {
	if allowed[t.Name] {
		kept = append(kept, t)
	}
}
```

In Openarity that choice is the brain's, made against the graph. This package
speaks the protocol; it does not decide.

## A runnable one

[`examples/mcp`](https://github.com/LaplacianAI/openarity/tree/main/sdk/agent/examples/mcp)
runs an MCP server in-process and points an agent at it, so it needs nothing
installed:

```text
$ go run ./examples/mcp
mcp      http://127.0.0.1:62501
  tool   repo__count_issues — Count the open issues in a repository.
  tool   repo__latest_release — The most recent release tag of a repository.

[step 1]
  → repo__count_issues({"repo":"openarity"})
  ← repo__count_issues: openarity has 3 open issues (513µs)
[step 2] openarity has 3 open issues.

answer   openarity has 3 open issues.
steps    2
tokens   504 in (0 cached), 24 out
```
