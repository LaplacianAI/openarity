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

## Layout

```text
apps/brain/
  cmd/brain/           the only binary: main(), run(), newLogger()
  internal/config/     configuration: load, validate, redact
  internal/server/     the two listeners: build, run, shut down
  internal/middleware/ request logging, and everything that wraps a handler
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

## Commands

```sh
make            # list targets
make check      # everything CI runs: tidy, fmt, vet, lint, build, test, vuln
make run        # run the server, Ctrl-C for a graceful shutdown
make cover      # coverage, fails below the threshold
make fmt        # apply gofumpt and fix import order
make tools      # reinstall tooling — rerun after a Go upgrade
```

`make check` is the real gate; run it before saying anything is done.

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
- **`/healthz` is not logged.** Kubernetes probes it every ten seconds on two
  listeners — roughly 17k lines a day that say nothing and bury everything
  else. The skip lives in the middleware and must still let the response
  through untouched.
