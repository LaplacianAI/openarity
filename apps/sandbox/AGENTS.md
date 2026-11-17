# sandbox

The pieces of Openarity's sandboxes that run outside the brain. One Go module,
one binary, roles as arguments:

| Role | What it is |
| --- | --- |
| `sandbox shim` | Inside a sandbox: serves an MCP server that speaks stdio over Streamable HTTP, so the brain can reach it at a URL |

The brain never imports this module and this module never imports the brain.
They meet only over HTTP: the brain asks a sandbox provider (E2B, Daytona, or
a self-hosted one) for a room, starts `sandbox shim … -- <command>` in it, and
speaks MCP to the shim's port. The design is in the openarity-ideation repo,
`Specs/2026-11-18-sandboxes-design.md`.

## Working agreement

The shim is **tier 2**: the agent writes it and the user reviews the design
before code. Design first: a high-level design with the user flow and its
diagrams, then a low-level design with its sequence diagram, approved before
code; every edit handed over with a link to its file and line.

## Layout

```text
apps/sandbox/
  cmd/sandbox/
    main.go         run(): parse, then the role; the shim serves until its server exits
    command.go      argument parsing: pure, no dependencies
  internal/shim/
    child.go        the server on stdio: one MCP session, ids of the shim's own, answers routed back
    sessions.go     the callers sharing it: session ids, idle expiry, requests in flight
    handler.go      the HTTP side: POST, DELETE, the bearer token, /healthz
```

## How the shim works

A stdio MCP server takes one client, and discovery and every run that calls
it open sessions of their own. So the shim is that one client:

- It runs the server once, initializes it once, and answers each session's
  `initialize` from that.
- Every request is forwarded under an id of the shim's own, and its answer
  goes back under the caller's id. A cancellation is passed on under the
  server's id.
- Requests from the server (sampling, elicitation, `roots/list`) are refused
  with `-32601`, and its notifications are dropped: no client is there to
  answer them.
- If the server exits, every waiting request fails and the shim exits
  non-zero, so the room is seen as failed rather than idle.

One process per room, shared, is the point. A bridge that starts a server per
session or per request has to clean up a process on every crash, timeout and
reconnect; Supergateway's open issues are mostly that.

## Commands

```sh
make            # list targets
make check      # tidy, format, vet, lint, build, cover, vuln: what CI runs
make test       # go test -race ./...
make build      # ./bin/sandbox
make fmt        # gofumpt and import order
make tools      # golangci-lint and govulncheck, at the versions CI uses
```

## Conventions

The brain's, where they apply: `os.Exit` only in `main`, which holds no
defers; the context and the writer are parameters; the logger is a
parameter and a nil one panics; tests reserve a real ephemeral port and poll
rather than sleep.

- **Tests run a real server.** The test binary re-runs itself as a stdio MCP
  server (`TestMain` and `fake-mcp-server`), and the Go MCP SDK's own client,
  the one the brain uses, drives the shim. A shim that only passes against a
  hand-written client proves nothing about the brain.
- **Every SDK call in a test has a deadline** (`quick`). An answer routed to
  the wrong caller never arrives, and a test without a deadline hangs instead
  of failing.
