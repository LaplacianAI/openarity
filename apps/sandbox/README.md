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

## Build and test

```sh
make build      # ./bin/sandbox
make check      # what CI runs
```

Requires Go 1.27.4. The tests need nothing else: the test binary plays the
MCP server.

For working on the module, read [AGENTS.md](AGENTS.md).
