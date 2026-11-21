# sandbox

The parts of Openarity's sandboxes that run outside the brain. Today that is
the shim: it runs inside a sandbox and serves an MCP server that speaks stdio
over Streamable HTTP, so the brain can reach it at a URL.

```sh
SHIM_TOKEN=… sandbox shim --listen :8080 -- npx --offline mcp-mail-server@2.1.3
```

- `POST /mcp`: one JSON-RPC message, answered as JSON. Every caller sends
  `Authorization: Bearer $SHIM_TOKEN`.
- `DELETE /mcp`: ends the caller's session.
- `GET /healthz`: `200` once the server has answered `initialize`.

It starts the server once and shares it between every caller. It exits
non-zero when the server does.

## sandboxd

`sandbox serve` is the optional self-hosted sandbox provider: it makes rooms
on a (rootless) Docker daemon, runs the shim in them, and gives each one an
egress guard that lets it reach only the hosts it lists.

```sh
SANDBOXD_TOKEN=… SANDBOXD_PUBLIC_URL=http://sandboxd:9090 \
SANDBOXD_DOCKER_SOCKET=/run/user/1000/docker.sock sandbox serve --listen :9090
```

`POST /v1/rooms`, `POST /v1/rooms/{id}/start`, `POST /v1/rooms/{id}/stop`,
`POST /v1/rooms/{id}/resume`, `GET /v1/rooms/{id}/ports/{port}`,
`ANY /v1/rooms/{id}/ports/{port}/proxy/...`, `DELETE /v1/rooms/{id}` and
`GET /healthz`. A room's image must be under a repository prefix in
`SANDBOXD_IMAGES` (default `ghcr.io/laplacianai/openarity-sandbox`), or the
create is refused with `422` before anything is made; sandboxd pulls an
allowed image it does not have when the room starts, and answers `503`
naming the image if it cannot. `SANDBOXD_PULL` lists images to pull in the
background at start. Deploy it with `deployment/docker-compose.sandboxd.yml`, or
`deployment/k8s/sandboxd/`. Settings are `SANDBOXD_*`; `sandbox serve` with
none set prints which are required.

A workspace room's `/workspace` is a Docker volume, `oa-sb-<room id>`, so a
stopped room keeps its files and is resumed with them; the volume is removed
with the room. A stopped room holds no memory: `SANDBOXD_MEMORY_BUDGET` counts
only running rooms, and a resume that does not fit answers `503`. A create may
ask for a size up to `SANDBOXD_WORKSPACE_MEMORY`, `SANDBOXD_WORKSPACE_CPUS`
and `SANDBOXD_WORKSPACE_BYTES` (the disk), and is refused with `422` past
them. Disk is a soft cap: every 30 seconds sandboxd measures the volumes and
stops a room past its disk, and every volume together is held to
`SANDBOXD_DISK_BUDGET` (default 8 GiB).

## Build and test

```sh
make build      # ./bin/sandbox
make check      # what CI runs
```

Requires Go 1.27.4. The tests need nothing else: the test binary plays the
MCP server. The integration tests, which run Docker, are skipped unless
`SANDBOXD_TEST_DOCKER` names a Docker socket.

For working on the module, read [AGENTS.md](AGENTS.md).
