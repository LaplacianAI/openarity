# Openarity

[![ci](https://github.com/LaplacianAI/openarity/actions/workflows/ci.yml/badge.svg)](https://github.com/LaplacianAI/openarity/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.work)

An open-source agent platform where agents, tools, skills and learnings are a
knowledge graph, not a list.

> **Early.** There is no release, no agent runtime and no stable API yet. What
> exists is the service skeleton described below. The design is still moving —
> open an issue before building anything substantial on top of it.

## The idea

Most agent frameworks pick tools by ranking a flat list. Openarity picks by
traversing relationships.

Agents, tools, skills and past learnings are stored as a knowledge graph, and
the orchestrator uses agentic graph RAG to decide what a task needs — which
agent, which tools, which skills, which learnings from previous runs. What a
tool *relates to* is the signal: the skills that use it, the learnings derived
from it, the agents that hold it.

A planner turns a request into a workflow with branches, loops and retries.
Channels — Slack, Discord, Telegram, webhooks — are pluggable and all normalise
into one message format, so every channel works with every part of the system
without special cases.

## What works today

The `brain` service runs and is production-shaped, but it does not yet do
anything an agent platform does:

- Two HTTP listeners, one for the API and one for webhooks, with full timeouts
  and graceful shutdown on `SIGINT`/`SIGTERM`
- Liveness (`/healthz`) and readiness (`/readyz`) probes — readiness checks
  Postgres and returns 503 when it cannot reach it
- Structured logging, text with source in development and JSON elsewhere
- Environment-only configuration, validated at startup, with credentials
  redacted whenever config is printed
- A Postgres connection pool and versioned migrations behind an advisory lock,
  applied with `brain migrate up`

Not built yet: the graph, the planner, the agent runtime, channel adapters, the
CLI, the dashboard, authentication.

## Quick start

Requires Go 1.26.5 and a Postgres 13 or newer you can reach. CI runs against
Postgres 18.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain

go run ./cmd/brain migrate up    # create the schema
go run ./cmd/brain               # serve; Ctrl-C to shut down
```

Every setting has a working default, so neither command needs any environment.
In another shell:

```sh
curl -s 127.0.0.1:21120/healthz    # ok
curl -s 127.0.0.1:21120/readyz     # ready, or 503 if Postgres is unreachable
```

`brain migrate down` rolls the last migration back.

## Configuration

Environment only, every variable prefixed `OPENARITY_`:

| Variable                   | Default                     | What it does                     |
| -------------------------- | --------------------------- | -------------------------------- |
| `OPENARITY_ENVIRONMENT`    | `development`               | Selects the log format           |
| `OPENARITY_LOG_LEVEL`      | `info`                      | `debug`, `info`, `warn`, `error` |
| `OPENARITY_API_BIND`       | `127.0.0.1:21120`           | API listener address             |
| `OPENARITY_WEBHOOK_BIND`   | `127.0.0.1:21121`           | Webhook listener address         |
| `OPENARITY_POSTGRES_DSN`   | see below                   | Relational store                 |
| `OPENARITY_FALKOR_DB_URL`  | `redis://127.0.0.1:6380`    | Graph store — not used yet       |
| `OPENARITY_REDIS_URL`      | `redis://127.0.0.1:6379`    | Cache and queues — not used yet  |
| `OPENARITY_VAULT_ADDR`     | `http://localhost:8200`     | Secret store — not used yet      |
| `OPENARITY_OMNI_ROUTE_URL` | `http://localhost:20128/v1` | Model router — not used yet      |

The Postgres default is:

```text
postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable
```

Secrets are never stored in Postgres or the graph. Rows hold Vault path
references only.

## Repository layout

```text
apps/brain/     the Go backend
  cmd/brain/    entrypoint, argument parsing, the serve and migrate commands
  internal/     config, server, middleware, store
go.work         ties every Go module in the repository together
```

A monorepo: the CLI, the dashboard and the deployment manifests will live here
too, because they all sit on one contract — the OpenAPI spec the brain
generates from its own routes. Channel clients that ship through an app store
get their own repositories.

## Development

```sh
cd apps/brain
make            # list targets
make tools      # golangci-lint, govulncheck, gopls, goose
make check      # everything CI runs: tidy, format, vet, lint, build, test, vuln
```

Tests that need Postgres skip unless `BRAIN_TEST_POSTGRES_DSN` is set, so
`make check` works with nothing running. CI always sets it.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## Contributing

Pull requests are welcome. For anything larger than a bug fix, open an issue
first — the design still moves.

- [Contributing guidelines](CONTRIBUTING.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md) — never report a vulnerability as a public issue

## License

[MIT](LICENSE).
