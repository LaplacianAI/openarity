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

Two things run: the `brain` service, and `oa`, the CLI that talks to it.

The brain is production-shaped, but it does not yet do anything an agent
platform does:

- Two HTTP listeners, one for the API and one for webhooks, with full timeouts
  and graceful shutdown on `SIGINT`/`SIGTERM`
- Liveness (`/healthz`) and readiness (`/readyz`) probes — readiness checks
  Postgres and returns 503 when it cannot reach it
- Structured logging, text with source in development and JSON elsewhere
- Environment-only configuration, validated at startup, with credentials
  redacted whenever config is printed
- A Postgres connection pool and versioned migrations behind an advisory lock,
  applied with `brain migrate up`
- Type-safe queries generated from SQL by sqlc, and a transaction helper that
  rolls back on error or panic
- Authentication against an OIDC provider, or a shared token for development.
  A verified caller becomes a user row on first sight, so nobody is
  pre-provisioned
- Role-based authorisation. Roles and their permissions are rows, so adding a
  role is a migration rather than a release
- A teams API — create a team, list them, and manage membership

`oa` is early too, but it is real:

- Named contexts — one brain and the credential it issued, kept together
  because a token is only valid where it came from. Create, rename, switch,
  delete
- Settings that say where they came from, so "I set it and it did not take" is
  answerable. A flag beats an environment variable beats the config file beats
  the built-in
- `--output table|json|yaml` on every command, so output is readable or
  parseable without a second tool
- Against a development brain it needs no setup: it finds the shared token in
  your shell, and only ever sends it to a loopback address

Not built yet: the graph, the planner, the agent runtime, channel adapters, the
dashboard. `oa` cannot log in against a real provider yet, and does not reach
the teams API — both are next.

## Quick start

Requires Go 1.26.5 and a Postgres 13 or newer you can reach. CI runs against
Postgres 18.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain

go run ./cmd/brain migrate up    # create the schema

# Serving needs a way to authenticate callers. For a local run, a shared token
# is enough; name yourself a super admin to get past authorisation as well.
export OPENARITY_DEV_TOKEN=letmein
export OPENARITY_SUPER_ADMINS=dev

go run ./cmd/brain               # serve; Ctrl-C to shut down
```

Every other setting has a working default. The service refuses to start with no
authentication configured rather than serving an open API — set
`OPENARITY_DEV_TOKEN` for development, or `OPENARITY_OIDC_ENABLED` and
`OPENARITY_OIDC_ISSUER` against a real provider.

In another shell:

```sh
curl -s 127.0.0.1:21120/healthz    # ok
curl -s 127.0.0.1:21120/readyz     # ready, or 503 if Postgres is unreachable

# Everything else needs a token. Without one: 401.
curl -s -H 'Authorization: Bearer letmein' 127.0.0.1:21120/whoami
# {"kind":"dev","issuer":"dev","subject":"dev","teams":[]}

curl -s -H 'Authorization: Bearer letmein' \
     -d '{"name":"platform"}' 127.0.0.1:21120/teams
```

`brain migrate down` rolls the last migration back.

## The CLI

`oa` talks to a brain over the same HTTP API. Against a development brain it
needs no setup — it finds `OPENARITY_DEV_TOKEN` in your shell:

```sh
cd openarity/apps/cli
make install                     # puts oa on your PATH, at $(go env GOPATH)/bin

oa whoami                        # the brain at 127.0.0.1:21120, unconfigured
```

More than one brain is a context — an address and the credential that brain
issued, kept together because a token is only valid where it came from:

```sh
oa context create staging --server https://brain.staging.example.com
oa context use local
oa context list
# * local    http://127.0.0.1:21120             no token
#   staging  https://brain.staging.example.com  no token
```

Every command takes `--output table|json|yaml`, and `oa config` makes a choice
stick:

```sh
oa context list -o json | jq -r '.[].server'
oa config set output json        # or OPENARITY_OUTPUT=json for one shell
```

`oa config show` reports every setting and where it came from, which is the
answer to "I set it and it did not take":

```text
context  local                   (~/.config/openarity/config.yaml)
server   http://127.0.0.1:21120  (~/.config/openarity/config.yaml)
theme    auto                    (default)
output   table                   (default)
token    not set
```

A flag beats an environment variable, which beats the config file, which beats
the built-in. The token value is never printed — not truncated, not masked.

## API

Everything except the probes requires `Authorization: Bearer <token>`.

| Endpoint                                  | Who may call it              |
| ----------------------------------------- | ---------------------------- |
| `GET /healthz`, `GET /readyz`             | anyone, unauthenticated      |
| `GET /whoami`                             | any authenticated caller     |
| `POST /teams`                             | super admins                 |
| `GET /teams`                              | every team, or your own      |
| `GET /teams/{id}`                         | members, and super admins    |
| `GET /teams/{id}/members`                 | members, and super admins    |
| `POST /teams/{id}/members`                | `member:write` in that team  |
| `DELETE /teams/{id}/members/{userID}`     | `member:write` in that team  |

A team you are not in is a 404, never a 403 — a 403 would confirm the id exists.

Listings are paged and take `?limit=` (default 50, maximum 100) and `?cursor=`:

```json
{"items": [], "next_cursor": "eyJjIjoiMjAyNi0wOC0xMVQxNToxNToyNi4uLiJ9"}
```

`next_cursor` is present only while more rows remain; its absence is the end of
the collection. Pass it back as `?cursor=` to fetch the next page.

## Configuration

The brain is configured by environment only — no files, no flags — every
variable prefixed `OPENARITY_`:

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
| `OPENARITY_OIDC_ENABLED`   | `false`                     | Verify tokens against an IdP     |
| `OPENARITY_OIDC_ISSUER`    | empty                       | Issuer URL, required if enabled  |
| `OPENARITY_OIDC_AUDIENCE`  | `openarity`                 | Audience the token must carry    |
| `OPENARITY_DEV_TOKEN`      | empty                       | Shared token — development only  |
| `OPENARITY_SUPER_ADMINS`   | empty                       | Comma-separated token subjects   |

The Postgres default is:

```text
postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable
```

Secrets are never stored in Postgres or the graph. Rows hold Vault path
references only.

`OPENARITY_DEV_TOKEN` is a single shared secret compared in constant time. It
exists so a development machine does not need an identity provider, and it has
no place in a deployment — use OIDC there. `OPENARITY_SUPER_ADMINS` lists token
subjects, not email addresses, and a super admin bypasses every team-scoped
check.

The CLI reads its own variables, and unlike the brain it also has a config
file. Each one overrides the file for one shell:

| Variable              | Default           | What it does                    |
| --------------------- | ----------------- | ------------------------------- |
| `OPENARITY_SERVER`    | `127.0.0.1:21120` | Which brain to talk to          |
| `OPENARITY_TOKEN`     | empty             | The credential to send          |
| `OPENARITY_OUTPUT`    | `table`           | `table`, `json`, `yaml`         |
| `OPENARITY_THEME`     | `auto`            | `auto`, `dark`, `light`         |
| `OPENARITY_DEV_TOKEN` | empty             | Sent only to a loopback address |

`oa` sends `OPENARITY_DEV_TOKEN` only when the resolved server is loopback.
Letting the server decide would mean any host reached by a typo could claim to
be in development and collect the secret from your shell.

The config file lives at `oa config path` — `~/.config/openarity/config.yaml`
on Linux, `~/Library/Application Support/openarity/config.yaml` on macOS. It is
written with `oa config` and `oa context`, so it never needs a text editor.

## Repository layout

```text
apps/brain/     the Go backend
  cmd/brain/    entrypoint, argument parsing, the serve and migrate commands
  internal/     config, server, middleware, store
  api/          openapi.yaml — the contract, hand-written
apps/cli/       oa, the command-line client
  cmd/oa/       the commands
  internal/     config, contexts, credentials, output formats
  internal/client/  generated from the brain's spec by oapi-codegen
deployment/     manifests
go.work         ties every Go module in the repository together
```

A monorepo, because every app sits on one contract —
`apps/brain/api/openapi.yaml`.
It is hand-written and reviewed as a diff rather than generated from code, so a
change to what callers may rely on is as hard to sneak past review as a
migration. The CLI's client is generated *from* it, and CI fails if the
committed client and the spec disagree.

Each app is its own Go module, so `apps/brain/internal/` is unreachable from
`apps/cli/` by construction. Nothing is shared but the spec. The dashboard will
join them; channel clients that ship through an app store get their own
repositories.

## Development

Each module has its own Makefile and its own CI job.

```sh
cd apps/brain
make            # list targets
make tools      # golangci-lint, govulncheck, gopls, goose
make check      # everything CI runs: tidy, generate, format, vet, lint, build, cover, vuln
make generate   # regenerate the sqlc query code after changing a .sql file
```

```sh
cd apps/cli
make check      # the same gate, no database needed
make generate   # regenerate the client after the brain's spec changes
make install    # put oa on your PATH
```

Tests that need Postgres skip unless a database is named, so the brain's
`make check` works with nothing running. Point them at one with
`make check db=postgres`; CI does the same against a service container.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## Contributing

Pull requests are welcome. For anything larger than a bug fix, open an issue
first — the design still moves.

- [Contributing guidelines](CONTRIBUTING.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md) — never report a vulnerability as a public issue

## License

[MIT](LICENSE).
