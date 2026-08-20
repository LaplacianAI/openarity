# brain

The Openarity backend. One Go module, one binary, two listeners.

What it does today, the API surface and every environment variable are
documented in the [root README](../../README.md) — this file is for working on
the module itself.

## Build and run

```sh
make run        # sources .env if present, Ctrl-C to shut down gracefully
make build      # compile into ./bin
```

Requires Go 1.26.6 and a Postgres 13 or newer. CI runs against Postgres 18.

`make run` needs a database and a way to authenticate callers. The service
refuses to start with no authentication configured rather than serving an open
API, so the smallest working `.env` is:

```sh
OPENARITY_DEV_TOKEN=letmein
OPENARITY_SUPER_ADMINS=dev
```

`.env.example` documents the rest. Nothing loads it automatically — `make run`
sources it with `set -a`, which is what turns a sourced file into environment.

## The database

```sh
make testdb db=openarity_test    # create the local test database, once per machine

go run ./cmd/brain migrate up    # apply migrations
go run ./cmd/brain migrate down  # roll the last one back
```

Migrations are embedded in the binary and applied by it, never by the `goose`
CLI — the CLI reads files from disk while the binary carries its own copy, and
the two drift silently. In Kubernetes it is a Job that completes before the
Deployment rolls.

```sh
make migration name=add_tenants  # scaffold a timestamped migration
```

## The gate

```sh
make check db=postgres    # tidy, generate, format, vet, lint, build, test
```

Run it before saying anything is done. CI runs the same thing plus `make cover`
and `make vuln` against a Postgres service container, in the `brain` job of
[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml).

**Always pass `db=` when measuring coverage.** Tests that need a database skip
when `BRAIN_TEST_POSTGRES_DSN` is empty, and `db=name` is what sets it. Without
it, `serve`, `migrateUp` and every query read 0% and the total drops from 97.6%
to 77.8% — which looks like a coverage problem and is not one. `make cover`
warns when the variable is unset for exactly this reason.

```sh
make cover db=postgres        # coverage, fails below the threshold
make cover-html db=postgres   # the annotated HTML report
make vuln                     # govulncheck
make fmt                      # apply gofumpt and fix import order
make tools                    # reinstall tooling — rerun after a Go upgrade
```

That skip is also what keeps `make check` useful with nothing running: on a
machine with no Postgres, the database tests skip rather than fail.

## Generated code

`internal/store/db` is generated from the migrations and
`internal/store/queries/*.sql` by [sqlc](https://sqlc.dev), and is
**committed**.

```sh
make generate         # regenerate
make generate-check   # regenerate and fail if the committed copy differs
```

Never edit a generated file, and never hand-write a `pool.Query` outside that
package.

## The API contract

`api/openapi.yaml` is **hand-written**, not generated from code. It is the
contract the CLI's client is generated from, and it is reviewed as a diff — a
change to what callers may rely on should be as hard to sneak past review as a
migration.

A test fails when the spec and the registered routes disagree, so a new
endpoint cannot land undocumented.

## Layout

```text
cmd/brain/           main(), argument parsing, the serve and migrate roles
internal/config/     configuration: load, validate, redact
internal/server/     the two listeners; mounts Routers
internal/middleware/ request logging, authentication, user resolution
internal/auth/       token in, Principal out — no database
internal/authz/      Can, CanInAnyTeam, the five scopes, the route table
internal/api/        Router, WriteJSON, DecodeJSON, Page
  <domain>/          one package per domain, each its own Router
internal/store/      Postgres: pool, migrations, queries
  migrations/        goose .sql files, embedded into the binary
  rbac.json          the permissions, roles and route mappings we ship
```

One binary. If a worker appears it becomes an argument (`brain worker`), not a
second `cmd/` — two binaries means every dependency bump is two images that can
drift.

`cmd/brain` is the composition root and the only place that knows every
dependency. `internal/server` never learns what Postgres is; `internal/api` will
never learn what a port is.

## Conventions and decisions

`CLAUDE.md` holds the house style, the layout rationale, and the decisions worth
not relitigating. `.claude/skills/` holds the step-by-step guides for adding an
environment variable, a route, a middleware, a migration or a query.
