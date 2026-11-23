---
title: MCP servers as tools
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

!!! warning

    **A child process inherits nothing unless you say so.** Go's `exec` gives a
    `nil` environment the parent's *entire* environment, so a stdio server you just
    launched would see every credential this process holds. `Env` is passed through
    verbatim and an unset one means empty, not inherited.

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

## Images and files a tool returns

A tool that answers with an image or a PDF hands it to the model. Each MCP
tool is a `Tool` with `Result`, and what it returned besides text is
attached: the model reads it in the turn after the tool's result, under a
line naming the call that attached it and saying it is the tool's output, not
the person's words.

| What the tool returns | What the model gets |
| --- | --- |
| Text | The text, in order |
| A PNG, JPEG, GIF or WebP image, as an image block or an embedded blob | The image, up to 5 MiB |
| A PDF as an embedded blob | The file, up to 10 MiB, named from its URI |
| An embedded resource holding text, or plain text, SVG, JSON, CSV, YAML, XML or source code as a blob (any `text/` type, `+json`, `+xml`) | Its text, in the tool's own text under a line naming it, the first 100 KiB of UTF-8 |
| Anything else: audio, another image type, a zip, a resource link | A line saying what it was and why it is not passed |

A file is attached only when its bytes are what the server says they are,
and an empty one never is. Each file attached is named in the text too, as
`[attached image-1.png: image/png, 220 KiB]`; a call passes on eight files
and 10 MiB at most, text files among them, and past that, or past a size,
the file is a line saying so. A name comes from the last part of the URI's
path, never its query, its fragment or a password, without control
characters, line separators or brackets, and at most 100 characters; an
image's gets its type's extension when it has none. A media type that is
not `type/subtype` is named as no type, and `image/jpg` is read as
`image/jpeg`. An empty file is named as empty. A result marked `IsError` is its text alone: a file that came
with it is named, never attached. Nothing is dropped without a word, since an
empty answer reads to a model as a tool that did nothing, and it calls it
again.

The limits are exported, `MaxImageBytes`, `MaxDocBytes`, `MaxTextBytes`,
`FilesPerCall` and `BytesPerCall`. Without a keeper, the bytes travel in the
transcript, as `Blob.Data`, and every later request sends them again.

### Keeping a tool's files

Set `Server.Keep` to a `Keeper` and every file a tool returns, readable or
not, is handed to it before the model sees anything: an image, audio, an
embedded blob, and text embedded as text. The keeper stores the file and
says how to name it:

```go
type Keeper interface {
	Keep(ctx context.Context, f mcp.File) (mcp.Kept, error)
}

// File{Tool, Name, MediaType, Data}: MediaType is what the server declared.
// Kept{ID, Ref}: the id the model is told, and the Ref a Blob carries.
```

The result then holds no file's bytes. Each file's line carries its id, and
an image or a PDF the model may see is attached as a `Blob` with the keeper's
`Ref` and no `Data`, for a `ModelClient` that fetches it per request:

```text
[attached shot.png: image/png, 220 KiB, file 1f2e…]
[report.csv: text/csv, 2.0 MiB, the first 100 KiB, file 77c1…]
<the first 100 KiB>
[bundle.zip: application/zip, 12.0 MiB, which a model cannot read, file 3d4e…]
[huge.zip: application/zip, 31.0 MiB, more than the 25.0 MiB a tool's file is kept up to]
[a.png: image/png, 1.0 MiB, which could not be kept]
```

Which files the model may see, and how, is as without a keeper; a text file
is kept whole and shown cut. A call keeps eight files at most, and each up to
`MaxKeptBytes`, 25 MiB. A keeper that returns an error does not fail the call:
the tool's work is done, so its text stands and the file is named as not
kept. The keeper's error is never shown to the model, since it may name a
store; log it there. The call's id is on the context, `agent.ToolCallID(ctx)`,
for a keeper that records which call returned a file.

## The tool list is read once

At connect, not per call. MCP servers may notify that their tools changed;
honouring that mid-run changes what the agent already decided to do, and for
[ReWOO](patterns.md#rewoo) it would change the ground under a plan that is
already fixed. Reconnect between runs instead.

Pagination is followed to the end. A server with more tools than fit in one
response is ordinary, and stopping at the first page loses the rest silently.

## How many tools is too many

Enough that it is the reason this project exists. Measured tool selection
[collapses as the pool grows](../concepts/why-a-graph.md); a few ordinary servers put
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
