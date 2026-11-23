# sandbox

The pieces of Openarity's sandboxes that run outside the brain. One Go module,
one binary, roles as arguments:

| Role | What it is |
| --- | --- |
| `sandbox serve` | sandboxd, the optional self-hosted provider: rooms on a rootless Docker daemon, an egress guard for each, and a proxy to their ports |
| `sandbox shim` | Inside a sandbox: serves an MCP server that speaks stdio over Streamable HTTP, so the brain can reach it at a URL |
| `sandbox workspace` | Inside a sandbox: an agent's Bash, Read, Write, Edit, Glob and Grep, over HTTP behind the room's token |

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
  internal/workspace/
    exec.go         a command in its own process group, killed when it ends or times out; each output's two ends kept
    files.go        read (numbered lines, binary refused), write, edit (one match unless replace_all)
    search.go       glob with **, grep with RE2; .git and node_modules left out
    handler.go      the routes, the bearer token, a mistake the model can fix as 422
  internal/shim/
    child.go        the server on stdio: one MCP session, ids of the shim's own, answers routed back
    sessions.go     the callers sharing it: session ids, idle expiry, requests in flight
    handler.go      the HTTP side: POST, DELETE, the bearer token, /healthz
  internal/api/     sandboxd's HTTP: the rooms API (bearer), the proxy to a room's port (X-Sandboxd-Token)
  internal/rooms/   rooms on Docker: create, start, delete, the memory budget, the image allow-list and pulls, Harden (every container's fixed settings)
  internal/guard/   the egress guard: a listener per room and port, SNI routing, the private-address refusal; proxy.go, a public room's forward proxy
  internal/docker/  the Engine API over the unix socket, with net/http
  internal/integration/  tests against a real daemon; skipped unless SANDBOXD_TEST_DOCKER names its socket
  Dockerfile        sandboxd; the room image is deployment/sandbox/Dockerfile
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
- **A new workspace that does not fit the disk budget evicts.** `Start` is the
  only caller of `workspaceVolume`. If the budget is spent it takes
  `Service.mu` and plans first: the stopped
  workspaces (a container that exists, is not running and has a stop time,
  read with `Engine.ContainerState`) in order of stop time, ties by id, as few
  as cover the gap, or none if they cannot, and then it is `ErrBusy` with
  nothing deleted. The plan is deleted with `teardown` on a context that
  ignores the caller's, then measured again. Running rooms, rooms with a
  volume and no container, and the room being started are never chosen. Resume
  does not evict. Sandboxd does not know teams, so a victim may be any team's.
  A workspace that fits the budget is made without the lock. Only one that
  does not fit takes it, measures again, and holds it from there to
  `CreateVolume`, victims' deletes included, so a slow disk walk delays other
  creates and resumes only then, as Resume already does, and not on every
  start.
  A volume whose container and network are gone is not reclaimed by eviction,
  and a container that exited on its own counts as stopped.
- **Run it against a rootless daemon**, so root in a room is an unprivileged
  user on the host. `deployment/docker-compose.sandboxd.yml` says how.

## How a public room reaches the internet

A room asked for with `"network": "public"` lists no hosts. The guard serves a
forward proxy on sandboxd's address in the room's network, on port 3128:
CONNECT for TLS and anything that tunnels, an absolute URL for plain HTTP. The
room's `HTTP(S)_PROXY` points at it. The room still has no route and no DNS of
its own, so the proxy resolves every name and the guard's dialer refuses a
private, loopback or link-local address after resolving. A program that
ignores the proxy reaches nothing, which is a refusal, never a leak.

A room asked for with `"workspace": true` gets a `/workspace` tmpfs it may run
from, and `SANDBOXD_WORKSPACE_MEMORY` (1.5 GiB) and `SANDBOXD_WORKSPACE_CPUS`
(one) in place of a server's limits. The budget sums each room's own memory.

## How the workspace works

`sandbox workspace` is the room's main process. Each command runs with
`bash -c` in `/workspace`, as its own process group, and the whole group is
killed when the command ends or times out (270 seconds at most), so what it
leaves in the background goes with it; a process that leaves the group
(`setsid`) lives until the room goes, within its limits. stdout and stderr
each keep their first and last 128 KiB. The process is made undumpable
(`workspace.Harden`) and the token is taken out of the environment before any
command runs, so no command reads it from its own environment or from the
workspace's `/proc/<pid>/environ`; the file tools refuse `/proc`, since they
run in the workspace process itself.
A path is relative to `/workspace` unless absolute, with its links followed:
the room is the boundary, not the directory, and `/proc` the one place the
file tools refuse.

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
