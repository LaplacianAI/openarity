# Contributing to Openarity

Thanks for taking the time. This document is short on purpose — most of it is
the path to running one command, `make check`, which is what CI runs.

Openarity is early. The design still moves. **For anything larger than a bug
fix, open an issue first** — it saves you building against something that is
about to change.

## Prerequisites

| Tool     | Version | Notes                                                  |
| -------- | ------- | ------------------------------------------------------ |
| Go       | 1.26.5  | pinned by `go.work` and each app's `go.mod`            |
| make     | any     |                                                        |
| Postgres | 13+     | optional — database tests skip without one; CI runs 18 |

## Getting set up

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain
make tools
make check
```

`make tools` installs `golangci-lint`, `govulncheck`, `gopls` and `goose` into
your `GOPATH/bin`. Rerun it after a Go upgrade.

`make` on its own lists every target.

## Repository layout

```text
apps/brain/        the Go backend — the only app that exists today
go.work            ties every Go module in the repository together
```

`apps/cli`, `apps/dashboard` and `deploy/` are planned and not here yet.

Each app is **its own Go module**, tied together by `go.work` at the root. Run
`make` from inside the app directory, not from the repository root. `go build
./...` at the root builds every Go module in the workspace and ignores
directories with no Go files, so the dashboard's toolchain never collides with
Go's.

## Before you open a pull request

```sh
cd apps/brain
make check
```

That is `tidy-check`, `generate-check`, `fmt-check`, `vet`, `lint`, `build`,
`cover` and `vuln` — exactly what `.github/workflows/ci.yml` runs, in the same
order. If it passes locally it passes in CI.

Run it **with a database**. The coverage floor is 70%, and with the Postgres
tests skipping, the suite lands close enough to that floor that an unrelated
change can trip it.

Run `make fmt` to apply formatting rather than fixing it by hand — the project
uses `gofumpt` and `gci`, and both are enforced.

## Tests that need a database

Tests that talk to Postgres read `BRAIN_TEST_POSTGRES_DSN` and **skip** when it
is unset, so `make check` stays useful with nothing running.

Point them at a database by passing its name:

```sh
make check db=postgres
make check db=openarity_test host=db.local port=5433 user=alice
make cover-html db=openarity_test host=db.local port=5433   # annotated report
```

`host`, `port`, `user` and `sslmode` all have defaults; only `db` is required,
and passing it is what switches these tests on. Exporting
`BRAIN_TEST_POSTGRES_DSN` yourself works too.

There is deliberately no default database. The skip triggers on the variable
being *empty*, not on the server being unreachable — so a default would turn
"no Postgres installed" from a skip into a wall of failures.

CI always sets it from a Postgres service container, so these tests always run
there. Each test creates its own schema and drops it afterwards, so pointing
this at a scratch database is safe.

New database tests should follow `apps/brain/.claude/skills/test-with-postgres`.

## Running the server

Every setting has a working default, so this needs no environment:

```sh
cd apps/brain
make run
```

Configuration is environment-only and prefixed with `OPENARITY_`:

```sh
OPENARITY_LOG_LEVEL=debug OPENARITY_API_BIND=127.0.0.1:8080 make run
```

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
fix(brain): close the pool when migrate fails
docs(brain): record what the listener work settled
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
