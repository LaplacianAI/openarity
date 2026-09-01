# Contributing to Openarity

Thanks for taking the time. This document is short on purpose — most of it is
the path to running one command, `make check`, which is what CI runs.

Openarity is early. The design still moves. **For anything larger than a bug
fix, open an issue first** — it saves you building against something that is
about to change.

## Prerequisites

| Tool     | Version | Notes                                                  |
| -------- | ------- | ------------------------------------------------------ |
| Go       | 1.26.6  | pinned by `go.work` and each app's `go.mod`            |
| make     | any     |                                                        |
| Postgres | 13+     | optional — database tests skip without one; CI runs 18 |

## Getting set up

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain
make tools
make check

cd ../cli
make tools
make check

cd ../../sdk/agent
make tools
make check
```

Each module installs its own tooling, because each pins its own versions. The
brain needs `golangci-lint`, `govulncheck`, `gopls` and `goose`; the CLI needs
`golangci-lint`, `govulncheck` and `oapi-codegen`; `sdk/agent` needs the first
two and nothing else. Rerun `make tools` after a Go
upgrade — anything installed with `go install` is compiled against the Go
present at the time, and breaks with a version-mismatch error until reinstalled.

`make` on its own lists every target.

## Repository layout

```text
apps/brain/        the Go backend
apps/cli/          oa, the command-line client
sdk/agent/         the agent loop, as a library
  examples/        runnable agents; `make example` runs every one of them
deployment/        manifests
go.work            ties every Go module in the repository together
```

`apps/dashboard` is planned and not here yet.

Each app is **its own Go module**, tied together by `go.work` at the root, and
so is `sdk/agent` — being a separate module is what makes it unable to import
`apps/brain/internal`, which is the rule that keeps an agent loop from
persisting or authorising anything. Run `make` from inside the module
directory, not from the repository root. `go build
./...` at the root builds every Go module in the workspace and ignores
directories with no Go files, so the dashboard's toolchain never collides with
Go's.

## Before you open a pull request

Run the gate in every module you touched.

```sh
cd apps/brain && make check db=postgres
cd apps/cli   && make check
cd sdk/agent  && make check
```

That is `tidy-check`, `generate-check`, `fmt-check`, `vet`, `lint`, `build` and
the tests — the same steps `.github/workflows/ci.yml` runs, in the same order,
as three jobs named `brain`, `cli` and `sdk`. CI adds `cover` and `vuln` on top.
If it passes locally it passes there.

`sdk/agent` has one step the others do not: `boundary`, which fails if the loop
or the core package links a provider SDK. Everything outside that module
arrives through an interface, and nothing but this check stops somebody
importing `openai-go` for one convenient type and making `ModelClient`
decorative.

Adding a reasoning pattern or a runnable example there has a skill each:
`sdk/agent/.claude/skills/add-a-loop` and `.../add-an-example`. Both record
traps that cost a debugging session the first time — dispatching a tool call
from a turn that was truncated at `MaxTokens`, and a stub gateway that repeats
its usage on every chunk so the run reports five times the tokens it spent.

Run the brain's **with a database**. The coverage floor is 70%, and with the
Postgres tests skipping the suite lands close enough to it that an unrelated
change can trip it. The CLI needs nothing running.

Run `make fmt` to apply formatting rather than fixing it by hand — the project
uses `gofumpt` and `gci`, and both are enforced.

## Tests that need a database

Tests that talk to Postgres read `BRAIN_TEST_POSTGRES_DSN` and **skip** when it
is unset, so `make check` stays useful with nothing running.

Point them at a database by passing its name:

```sh
make testdb db=openarity_test           # once per machine
make check  db=openarity_test
make cover-html db=openarity_test       # the annotated report
```

The database has to exist; `make testdb` creates it. Each test then makes its
own schema inside it and drops it afterwards, so a throwaway database is safe
and one database serves the whole suite.

To skip that step entirely, point at the `postgres` database every server
already has:

```sh
make check db=postgres
```

`db=` alone leaves `port` at 5432, so on a machine with Postgres installed
*and* Postgres in compose it reaches the installed one. That has cost real
hours here — see the version floor below.

`host`, `port`, `user` and `sslmode` all have defaults and take overrides —
`make check db=brain host=10.0.0.5 port=5433 user=alice`. Only `db` is
required, and passing it is what switches these tests on. Exporting
`BRAIN_TEST_POSTGRES_DSN` yourself works too.

There is deliberately no default database. The skip triggers on the variable
being *empty*, not on the server being unreachable — so a default would turn
"no Postgres installed" from a skip into a wall of failures.

CI always sets it from a Postgres service container, so these tests always run
there. Each test creates its own schema and drops it afterwards, so pointing
this at a scratch database is safe.

### The tests need Postgres 18; the brain needs 13

Two floors, and only one of them is about the product.

The brain needs **13**, because the first migration calls `gen_random_uuid()`
and 13 is where that became built-in — every migration applies and rolls back
cleanly on 13 through 18, and fails on 12 with `SQLSTATE 42883`.

The tests need **18**, because `schema_test.go` asserts `SQLSTATE 23001` for an
`ON DELETE RESTRICT` refusal, and 18 is the first release that raises it. Every
version from 13 to 17 reports the same refusal as `23503`, a plain foreign key
violation. Nothing in `internal/api` reads a code that moved — the only one
production matches on is `23505` — so this floor is the test suite's alone.

`internal/store` refuses to run below it, before any test, naming the version
*and the address it reached*:

```text
BRAIN_TEST_POSTGRES_DSN points at PostgreSQL 14.17 (Homebrew) on 127.0.0.1:5432.
These tests need 18 or newer: schema_test.go asserts SQLSTATE 23001, and 18 is
the first release that raises it. The brain itself runs on 13 or newer — this
floor belongs to the test suite, not to the product.
If a compose database is up, this is probably not it. Try:
    make check db=openarity_test port=15432
```

It names the address because the failure it exists for was not a wrong
assertion but a green run against a database nobody meant to use. It fails
rather than skipping for the same reason: a skip would have been the same
silence in a different colour.

New database tests should follow `apps/brain/.claude/skills/test-with-postgres`.

## Tests that need a secret store

The same shape, with two variables instead of one. Tests that talk to OpenBao
read `BRAIN_TEST_SECRETS_ADDR` and `BRAIN_TEST_SECRETS_TOKEN`, and **skip**
when either is unset:

```sh
docker run -d --name openbao -p 8200:8200 \
  -e BAO_DEV_ROOT_TOKEN_ID=dev-root \
  -e BAO_DEV_LISTEN_ADDRESS=0.0.0.0:8200 \
  openbao/openbao:2.6.2

export BRAIN_TEST_SECRETS_ADDR=http://127.0.0.1:8200
export BRAIN_TEST_SECRETS_TOKEN=dev-root
```

Both `BAO_` names matter. `VAULT_DEV_ROOT_TOKEN_ID` leaves the server running
with a randomly generated root token, so the tests fail with 403 instead of
skipping; and without `BAO_DEV_LISTEN_ADDRESS` the dev listener binds loopback
*inside* the container and the port mapping reaches nothing.

The token is a root token, and the tests use it only to set up the AppRole and
policy the store under test then authenticates with — the store itself never
sees it. Dev mode keeps everything in memory, so a restart is a clean slate.

`deployment/docker-compose.yml` runs the same thing as a service if you would
rather not manage the container by hand. CI runs an OpenBao service container,
so these tests always run there.

## Tests that need an object store

The same shape again. Tests in `internal/objects/s3` read
`BRAIN_TEST_S3_ENDPOINT` and **skip** when it is unset;
`BRAIN_TEST_S3_BUCKET`, `BRAIN_TEST_S3_ACCESS_KEY` and
`BRAIN_TEST_S3_SECRET_KEY` fill in the rest.

```sh
cd deployment && make objects   # MinIO on 19000, bucket created
```

It prints the four exports. 19000 rather than MinIO's own 9000, because
authentik already publishes 9000 in this stack.

The other two object backends need nothing: `inmemory` and `filesystem` are
tested with a `t.TempDir()` and always run. Only the S3 adapter has a belief
about somebody else's API to check, and the belief worth checking is what a
real store returns for a key that is not there — AWS answers with a typed
`NoSuchKey`, several clones answer with a bare 404, and a stub can assert
neither.

## Running the server

Ports and addresses all have working defaults, and they match what
`deployment/docker-compose.yml` publishes. Authentication does not — the brain
refuses to start with no way to identify a caller, so a bare run exits 1:

```sh
cd apps/brain
cp .env.example .env
make run
```

`make run` sources `.env` when it exists; nothing in the code reads it, because
configuration is environment-only by decision. The file is gitignored, so a
local token never reaches a branch.

Configuration is prefixed with `OPENARITY_` and can also be passed inline:

```sh
OPENARITY_LOG_LEVEL=debug OPENARITY_API_BIND=127.0.0.1:8080 make run
```

A setting present in `.env` wins over one passed that way — the file is
sourced after make already has its environment. Comment the line out in `.env`
rather than fighting it.

`internal/config/config.go` is the full list. Adding a setting is documented in
`apps/brain/.claude/skills/add-env-var`.

## Queries

SQL lives in `internal/store/queries/*.sql`. The Go that runs it is generated
by [sqlc](https://sqlc.dev) into `internal/store/db` and **committed** — never
edited by hand.

```sql
-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;
```

```sh
make generate
```

`:one` returns a row, `:many` a slice, `:exec` only an error. sqlc reads the
schema straight from the goose migrations, so there is no second copy to keep
in sync.

CI runs `make generate-check`, which regenerates and fails if the result
differs from what is committed. Change a query, run `make generate`, commit
both files together.

Writes that must all succeed or all fail go through `Store.InTx`, which hands
the callback a `*db.Queries` bound to the transaction.

## Migrations

```sh
cd apps/brain
make migration name=add_teams
```

Migrations are timestamped, applied with `brain migrate up`, and rolled back
with `brain migrate down` — never with the `goose` CLI, which does not take the
same advisory lock.

**Never edit a migration that has already been applied anywhere.** Write a new
one. Always write the `Down`. `apps/brain/.claude/skills/write-migration`
covers locking, expand–contract changes and batched backfills.

## Working on the CLI

`oa` needs no database and no services. Its gate is one command:

```sh
cd apps/cli
make check
make install    # put oa on your PATH to try it
```

`internal/client/client.gen.go` is generated from `apps/brain/api/openapi.yaml`
by [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) and
**committed** — never edited by hand.

```sh
make generate
```

CI runs `make generate-check`, which regenerates and fails if the result
differs from what is committed. That is the job that catches a spec change
landing in the brain without the client being rebuilt: change an endpoint, run
`make generate` in `apps/cli`, and commit both modules together.

Coverage there excludes `internal/client`. It is roughly two thousand generated
lines nobody wrote, so counting it measures oapi-codegen rather than the module.

Adding a command, a setting or an output format is documented in
`apps/cli/.claude/skills/`.

## Branches and pull requests

`main` is protected: no direct pushes, no force-pushes, and CI must pass.

1. Branch off `main` — `feat/…`, `fix/…`, `docs/…`, `chore/…`
2. Push and open a pull request against `main`
3. CI runs on every push to the branch
4. Pull requests are squash-merged, so the **pull request title becomes the
   commit subject** — write it as a real commit message
5. Branches are deleted automatically on merge

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org), scoped by app:

```text
feat(brain): serve the API and webhook listeners
feat(cli): contexts, and settings that say where they came from
fix(brain): close the pool when migrate fails
docs(cli): record why the dev token never leaves loopback
chore: bump checkout and setup-go to v7
```

Types in use: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`.

Subject in the imperative, no trailing period, under about 72 characters. Put
*why* in the body — the diff already shows *what*.

There is no CLA and no sign-off requirement.

## Reporting bugs and security issues

Bugs go in [issues](https://github.com/LaplacianAI/openarity/issues).

**Security vulnerabilities do not.** See [SECURITY.md](SECURITY.md).

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By taking
part you agree to uphold it.
