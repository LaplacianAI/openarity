# brain

The Openarity backend. One Go module, one binary, five packages drawn exactly
as services. Design lives in the
[openarity-ideation](https://github.com/LaplacianAI/openarity-ideation) repo,
under `Tech Design/HLD-V1/HLD.md`.

## Working agreement

**The user writes the production code. Claude writes the tests and tries to
break it, explains, and reviews.**

- Do not write implementation code, scaffold files, or finish a function unless
  explicitly asked. Guide step by step, method by method.
- When code is pasted: review it, list what is missing or wrong, then write
  tests that attack it — not tests that confirm it works.
- Explanations stay short and plain, and always include a small snippet.
  Functions in snippets stay small: one job, few lines, no cleverness.
- Verify library behaviour with a throwaway probe under the scratchpad rather
  than asserting it from memory. Both `net.SplitHostPort` and `url.Parse`
  turned out far more lenient than they look.

## Skills

- **`add-env-var`** — adding any new environment variable. Covers the struct
  field, the enum type if the value is a fixed set, validation, redaction in
  `String()`, and the three test files. Use it every time; a field wired into
  four of the five places is the normal failure.
- **`add-middleware`** — anything that wraps an `http.Handler`: auth, request
  IDs, panic recovery, rate limits, body size limits. Covers the signature,
  wrapping the response writer, chain order, and wiring into `server.New`. Use
  it every time; a middleware that is built and never applied passes every
  linter and every test except the wiring one.
- **`add-route`** — every API endpoint, and every new domain package under
  `internal/api`. Covers where the route lives, the `Router` and its prefix,
  per-package dependencies, the response contract, which status each failure
  gets, where the `Can` check goes, and the five tests a route owes. Use it
  every time; a route registered on the wrong mux passes every test written
  about its body.
- **`write-migration`** — every schema change. Covers the goose file format,
  `lock_timeout`, expand-contract for column changes, batched backfills, and
  when an index needs `CONCURRENTLY`. Use it every time: the migration that
  freezes a table in production is indistinguishable from a safe one when the
  table is empty.
- **`write-query`** — every query that reads or writes Postgres. Covers the
  sqlc annotations, regenerating and committing the output, type overrides,
  when a write needs `InTx`, and the batch and copy modes. Use it every time;
  never hand-write the Go that runs SQL.
- **`write-tests`** — every test, any package. Covers naming, when
  `t.Parallel()` is allowed, contexts and ports and timing, what a fake owes
  you, and step 7: break the thing and confirm the test fails. Read it before
  writing a concurrency test.
- **`fix-lint`** — any golangci-lint or gofumpt failure, and any change to
  `.golangci.yml`. Lists the linters that actually fire here and the correct
  fix for each. The fix is almost never a `nolint`.
- **`test-with-postgres`** — any test that needs a real database. Covers
  skipping when none is available, one schema per test, why these cannot be
  `t.Parallel()`, and how to check a test would actually fail. Read step 5
  before writing any concurrency test: we shipped one that passed with the
  guard removed.

## Layout

```text
apps/brain/
  cmd/brain/
    main.go            main(), run(), execute()
    command.go         argument parsing — pure, no dependencies
    serve.go           the serve role
    migrate.go         the migrate role
    logger.go          newLogger()
  internal/config/     configuration: load, validate, redact
  internal/server/     the two listeners: build, run, shut down; mounts Routers
  internal/middleware/ request logging, authentication, user resolution
  internal/auth/       token in, Principal out — no database
  internal/authz/      Can, and the closed vocabulary of actions
  internal/api/        Router, WriteJSON, DecodeJSON, Page
    <domain>/          one package per domain, each its own Router
      <domain>.go      handler, New, the handlers
      schema.go        request and response structs — the wire contract
  internal/store/      Postgres: pool, migrations, queries
    migrations/        goose .sql files, embedded into the binary
  Makefile             build and code quality targets
  .golangci.yml        linters and formatters
```

One binary. If a worker or another role appears it becomes an argument
(`brain worker`), not a second `cmd/`. Same config, same dependencies, one
image, one version — two binaries means every dependency bump is two images
that can drift.

`cmd/brain` is the composition root and the only place that knows every
dependency. `internal/server` never learns what Postgres is; `internal/api`
will never learn what a port is.

Each app in the monorepo is a separate Go module. `apps/brain/internal/` is
unreachable from `apps/cli/` by construction — Go's `internal` rule is scoped
to the module root. Nothing is shared between apps; the only thing crossing an
app boundary is `api/openapi.yaml`, and it is a spec, not code.

If brain and another app appear to need the same Go code, that is the signal
they should be talking over HTTP instead.

## Conventions

- **Initialisms keep one case**: `URL`, `DSN`, `API`, `ID`, `DB`. Unexported
  names starting with one go all-lowercase: `redactURL`, but `urlParser`.
  `revive`'s `var-naming` enforces this.
- **File names match content**: singular for one type (`config.go`), plural for
  several related things (`enums.go`). Split by job, not by size — loading,
  validating and the enums are three files.
- **A type, its constants and its methods stay together** in one file.
- **Interfaces only where a second implementation exists.** Methods on structs
  are the right amount of OOP here; an interface with one implementation just
  costs a hop when reading. Accept interfaces, return structs.
- **Do not create a package until it has an occupant.** No `util` package —
  name packages after what they do (`redact`, not `util`). A little copying
  beats a little dependency.
- **Tests mirror source files**: `validate.go` has `validate_test.go`.
- **Tests inject configuration, never mutate process env.** `load(map[...])`
  keeps tests isolated and `t.Parallel()`-safe; `t.Setenv` does not.
- **`os.Exit` appears only in `main`, and `main` holds no defers.** `os.Exit`
  skips deferred functions entirely, so a `defer` beside it is dead code. That
  is the entire reason `run(ctx, out)` exists — everything that needs cleanup
  lives there and returns an error instead. `gocritic`'s `exitAfterDefer`
  catches regressions.
- **Take the context and the writer as parameters.** Anything that builds its
  own context cannot be cancelled by a test, and anything that writes to
  `os.Stdout` directly cannot be observed by one. Both turn a fast test into a
  ten-minute timeout.
- **Tests reserve a real ephemeral port; they never hardcode one and never use
  port 0.** Bind `127.0.0.1:0`, read `l.Addr()`, close it, use the address.
  A hardcoded port collides with a running brain, and `checkHostPort` rejects
  port 0 outright.
- **`noctx` bans the context-free constructors.** Use
  `httptest.NewRequestWithContext`, `(*net.ListenConfig).Listen` and
  `exec.CommandContext` — never `httptest.NewRequest`, `net.Listen`,
  `http.Get` or `exec.Command`. In tests the context is `t.Context()`.
- **Lifecycle tests poll, they do not sleep.** Wait until `/healthz` answers
  200 before asserting anything, with a deadline that fails the test. A
  `time.Sleep` long enough to be reliable is long enough to be slow, and short
  enough to be fast is flaky in CI.
- **Use the stdlib type when it already encodes the constraint.** `LogLevel` was
  a hand-written enum until `slog.Level` turned out to be a `TextUnmarshaler`
  that parses case-insensitively and rejects unknown names with a better
  message. Write your own type when the constraint is yours — `Environment` has
  no stdlib equivalent and stays. Probe the stdlib before writing the enum.
- **The logger is a parameter, never a global.** `slog.Default()` is a hidden
  dependency and cannot be swapped safely under `t.Parallel()`. It reaches
  `internal/server` through `New`, and a nil one is left to panic rather than
  defaulted — see the trust rule below.
- **Log fields, not sentences.** `logger.Info("request", "status", 202)`, never
  `logger.Info(fmt.Sprintf("request returned %d", 202))`. The point is querying
  by field. Never log a whole struct: `"config", cfg` with `%+v` bypasses the
  `String()` that was redacting the password.
- **Log `r.URL.Path`, never `r.URL.String()`.** Credentials travel in query
  strings, and the log shipper takes them off the box.
- **Middleware returns a `Middleware`, it does not take `next` directly.**
  `func LogRequests(logger) Middleware` — dependencies in the outer call, the
  handler in the inner one. The two-argument form nests inside-out and becomes
  unreadable at three middlewares.
- **An exported identifier that nothing calls is invisible.** `unused` assumes
  an exported function has a caller in another package, so a middleware that is
  built and never applied passes every linter. Only a test that drives the wired
  object catches it.
- **Validate what comes from outside; trust what comes from the composition
  root.** `config.Load` validates because the environment is outside the
  program. `server.New` does not check its logger for nil because `run` is
  inside it — a nil there is a programming error, and a panic with a stack
  trace beats a silent fallback.
- **Cheapest failure first.** `run` parses arguments before loading config, and
  loads config before dialling Postgres. A typo in a Kubernetes Job spec should
  fail instantly with `unknown command "migrat"`, not after a connect timeout
  with an error blaming the database.
- **Parsing is separate from execution.** `parse` turns `[]string` into a
  `command` and is pure — no config, no database, no environment. `execute`
  validates nothing because `parse` already did. That is what lets the argument
  tests run with nothing set up.
- **A fixed set of strings is a defined type, not a `string`.** `commandName`
  and `direction` exist so `exhaustive` fails the build when a new role is
  added and a switch forgets it. That guarantee depends on
  `default-signifies-exhaustive: false` — it was `true` here for three steps,
  and adding a `commandName` reported 0 issues. Keep the `default:` arm anyway:
  it is unreachable, it returns a clear error rather than silence, and it is
  tested.
- **Never put a DSN in an error message.** pgx already redacts the password in
  its own errors — `postgres://user:xxxxx@…` — and wrapping with the raw string
  undoes that. Errors reach stderr and the log shipper.
- **Never delete error handling to raise a coverage number.** When a branch is
  untestable, say so in a comment and pin the assumption with a test that fails
  if it stops holding — see `TestSessionLockerCannotFailWithoutOptions`.

## Commands

```sh
make                    # list targets
make check db=postgres  # everything CI runs — see the note below about db=
make run                # run the server; sources .env if present, Ctrl-C to stop
make cover              # coverage, fails below the threshold
make cover-html db=postgres      # the annotated HTML report
make testdb db=openarity_test    # create a test database — once per machine
make generate           # regenerate everything generated (today: sqlc)
make fmt                # apply gofumpt and fix import order
make tools              # reinstall tooling — rerun after a Go upgrade
```

`make check` is the real gate; run it before saying anything is done.

**Always pass `db=` when measuring coverage.** Database tests skip when
`BRAIN_TEST_POSTGRES_DSN` is empty, and `db=name` is what sets it — `host`,
`port`, `user` and `sslmode` default around it. Without it `serve`, `migrateUp`
and every query read 0% and the total drops from 96.9% to 70.3%, which looks
like a coverage problem and is not one. `make cover` warns when the variable is
unset for exactly this reason. Never read a coverage report that was produced
without a database.

**After a Go toolchain upgrade, run `make tools`.** Anything installed with
`go install` is compiled against the Go present at the time, and both
`golangci-lint` and `gopls` break with a version-mismatch error until
reinstalled.

## Decisions worth not relitigating

- **Config is env-only.** No config files, no flags for the server, no Viper.
  Kubernetes injects env natively, and the config surface stays small because
  secrets live in Vault rather than here.
- **Postgres is truth, the graph is an index.** Nothing is written only to
  FalkorDB; every node has a Postgres row behind it and the graph is
  rebuildable at any time.
- **Two listeners, one process.** The API binds loopback; webhooks bind
  publicly. The auth models are opposites — user credentials versus request
  signature — so they never share a listener. Signature verification needs the
  raw request body, so nothing may parse it first. They never share a mux
  either: one handler on both ports exposes every API route publicly the day
  the routes diverge.
- **Listeners share a fate.** If one fails to bind, the other is shut down and
  the process exits non-zero. Half-alive is the worst state — the pod passes
  its probe while silently dropping webhooks, providers retry a few times and
  give up, and nothing is logged. Fail fast and let Kubernetes crash-loop it
  into an alert.
- **`http.ErrServerClosed` is success.** `ListenAndServe` always returns a
  non-nil error; a clean `Shutdown` returns that one. Treat it as a failure and
  every rolling update is recorded as a crash.
- **Server timeouts are constants, not configuration.** Go's zero-value
  `http.Server` has no timeouts at all, which leaves the public webhook port
  open to Slowloris — `gosec` G112 fails the build on a missing
  `ReadHeaderTimeout`. The same values are right in every environment, so they
  stay named constants. Promote one to config only when a real deployment needs
  a different value. Note that `WriteTimeout` bounds handler execution too: the
  streaming endpoints will need `http.NewResponseController(w).SetWriteDeadline`
  to opt out per route.
- **Secrets are references.** A row or config field holds a Vault path, never a
  value. Only `internal/secrets` imports a secret backend SDK.
- **`slog` from the stdlib.** No zap, no zerolog. Text with source locations in
  development because a terminal is the reader; JSON everywhere else because a
  log aggregator is. `AddSource` costs a stack walk per record, so development
  only.
- **`/healthz` and `/readyz` are not logged.** Kubernetes probes them every ten
  seconds on two listeners — roughly 17k lines a day that say nothing and bury
  everything else. The skip lives in the middleware, matches the path exactly,
  and must still let the response through untouched.
- **Liveness never checks a dependency; readiness always does.** `/healthz`
  answers 200 with Postgres on fire — failing it restarts every pod at once,
  which fixes nothing and adds a reconnect storm to the outage. `/readyz` pings
  the database and returns 503, which takes the pod out of the Service and
  restarts nothing. Both routes go on both listeners: `kubelet` probes the pod
  IP, so the loopback API listener is unreachable to it and the webhook copies
  are the ones Kubernetes actually uses.
- **pgx, sqlc and goose. No ORM.** Three tools, one job each, none hiding the
  database. The queue needs `FOR UPDATE SKIP LOCKED`, the runtime tables need
  `jsonb` operators, and graph RAG will need CTEs — all natural in SQL and all
  awkward through an ORM. sqlc's weakness is dynamic queries; write the two or
  three real shapes as named queries rather than reaching for a builder.
- **`pgxpool` is lazy.** `New` dials nothing — a stopped database, a wrong host
  and a wrong password all return a working pool and a nil error. `Ping` is the
  only proof, which is why `run` calls it before anything else starts.
- **Pool settings live in code, not the DSN.** `pool_max_conns` and friends in
  a connection string are silently overridden by `applyPoolDefaults`. One place
  decides, and it is greppable. pgx's own default derives `MaxConns` from
  `NumCPU`, so the same image would open 8 connections on one node and 64 on
  another.
- **Migrations are embedded and applied by the binary.** `brain migrate up`,
  never the `goose` CLI against a real database — the CLI reads files from
  disk while the binary carries its own copy, and the two drift silently. In
  Kubernetes it is a Job that completes before the Deployment rolls: never an
  `initContainer` on every pod, and never inside `run`.
- **The migration advisory lock is project-specific.** goose's `DefaultLockID`
  is crc32 of the string `"goose"` and is therefore shared by every goose user;
  Postgres advisory locks are scoped per database, not per schema. Ours is
  crc32 of `"openarity"`. Locking is also off unless `WithSessionLocker` is
  passed — the default is no lock at all.
