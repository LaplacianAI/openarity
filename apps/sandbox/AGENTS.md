# sandbox

The pieces of Openarity's sandboxes that run outside the brain. One Go module,
one binary, roles as arguments:

| Role | What it is |
| --- | --- |
| `sandbox serve` | sandboxd, the optional self-hosted provider: rooms on a rootless Docker daemon, an egress guard for each, and a proxy to their ports |
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
    command.go      argument parsing: pure, no dependencies; serve's settings from the environment
  internal/shim/
    child.go        the server on stdio: one MCP session, ids of the shim's own, answers routed back
    sessions.go     the callers sharing it: session ids, idle expiry, requests in flight
    handler.go      the HTTP side: POST, DELETE, the bearer token, /healthz
  internal/api/     sandboxd's HTTP: the rooms API (bearer), the proxy to a room's port (X-Sandboxd-Token)
  internal/rooms/   rooms on Docker: create, start, delete, the memory budget, the image allow-list and pulls, Harden (every container's fixed settings)
  internal/guard/   the egress guard: a listener per room and port, SNI routing, the private-address refusal
  internal/docker/  the Engine API over the unix socket, with net/http
  internal/integration/  tests against a real daemon; skipped unless SANDBOXD_TEST_DOCKER names its socket
  Dockerfile        targets: sandboxd (default), room (Node and the shim)
```

## How sandboxd works

It keeps no state the daemon does not hold. A room is a network `oa-sb-<id>`
whose labels carry its spec; `start` adds the container to it. Restart
sandboxd and `Reconcile` opens each room's egress guard again.

- **Every container is built by `rooms.Harden`**: read-only root, tmpfs
  `/tmp` and `/home/sandbox`, cap-drop ALL, no-new-privileges, default
  seccomp, user 65534, pids, memory and CPU limits, an `--internal` network of
  its own. A request chooses the image, env, labels and egress hosts, and
  nothing else; a body with any other field is refused with 422.
- **Only allowed images, pulled on demand.** `SANDBOXD_IMAGES` is a list of
  repository prefixes; `rooms.Allowed` matches on a boundary (the end, `:`,
  `@` or `/`, and a `:` followed by a `/` is a registry's port, not a tag), so
  `a/room` allows `a/room:1` and not `a/room-evil`. `Create` refuses any other
  image with 422 before the daemon is asked anything, and `Start` checks again
  because the spec outlives the setting. `Start` is where the image is first
  needed: it pulls through `docker.Client.PullImage` if `HasImage` says it is
  missing, once per image however many starts wait (`ensureImage`), on a
  context of its own so one caller giving up does not fail the others. The
  Engine API answers 200 and reports a failed pull as an `error` message in
  the stream, so `PullImage` reads the stream to its end. A failed pull is
  `ErrUnavailable`, 503 with the image named. `SANDBOXD_PULL` is warmed in a
  goroutine at start (`Service.Warm`) and only logged.
- **The egress guard** listens on sandboxd's address in the room's network,
  for each egress port. The room's `/etc/hosts` points each listed host at
  that address. One host on a port: that host. Several: the TLS SNI, and a
  name not listed is refused. A port whose protocol is not TLS from its first
  byte (25, 80, 587, ...) serves one host only; two are refused at create.
  The dialer refuses a private, loopback or link-local address after
  resolving, so a listed name that resolves inward gets nothing.
- **The proxy** to a room's port is authenticated by `X-Sandboxd-Token`, an
  HMAC of the room id under `SANDBOXD_TOKEN`, because `Authorization` is the
  brain's bearer token for the shim and must pass through untouched.
- **Run it against a rootless daemon**, so root in a room is an unprivileged
  user on the host. `deployment/docker-compose.sandboxd.yml` says how.

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
- **The integration tests run sandboxd in a container**, as it runs in
  production: it has to be a member of each room's network. They need the
  internet (the npm registry, one public host, and Docker Hub for the pull
  test, which removes `alpine:3.20` from the daemon).
  `SANDBOXD_TEST_DOCKER=<socket> go test ./internal/integration/`.
