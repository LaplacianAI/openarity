---
name: fix-lint
description: Fix a golangci-lint or gofumpt failure in the brain, or decide whether to change the lint configuration. Covers what is enabled and why, the specific linters that fire here and the correct fix for each, when a nolint is acceptable, and how to verify a configuration change actually catches what it claims to.
---

# Fix a lint failure

```sh
cd apps/brain
make fmt      # apply formatting — never fix gofumpt or import order by hand
make lint     # golangci-lint run
```

Formatting is never argued with: `make fmt` applies `gofumpt` and `gci`.
`make fmt-check` is the CI form and only reports.

## Step 1 — the fix is almost never a nolint

These linters were each enabled for a reason, listed in `.golangci.yml`. A
failure is nearly always the linter being right.

| Linter | What it caught here | The fix |
| --- | --- | --- |
| `noctx` | `httptest.NewRequest`, `net.Listen`, `exec.Command` in tests | the `...WithContext` / `ListenConfig` / `CommandContext` variant, with `t.Context()` |
| `gosec` G112 | `http.Server` with no `ReadHeaderTimeout` — Slowloris on the public webhook port | set all four timeouts; they are constants in `internal/server` |
| `gocritic` `exitAfterDefer` | `defer stop()` next to `os.Exit(1)` | `os.Exit` only in `main`, and `main` holds no defers — that is why `run()` exists |
| `errcheck` | `tx.Rollback(ctx)` in a defer | `defer func() { _ = tx.Rollback(ctx) }()` — genuinely nothing to do with it |
| `staticcheck` QF1008 | `s.Queries.WithTx(tx)` on an embedded field | `s.WithTx(tx)` |
| `revive` `var-naming` | `Url`, `Dsn`, `Api` | initialisms keep one case: `URL`, `DSN`, `API`, `ID`, `DB`. Unexported names starting with one go all-lowercase: `redactURL`, but `urlParser` |
| `exhaustive` | a switch that ignores a new enum value | add the case; see below |
| `errorlint` | `%v` on a wrapped error, `err == target` | `%w`, and `errors.Is`/`errors.As` |
| `nilerr` | `return nil` on a non-nil error | return the error |

Test files are excluded from `gosec` and `unparam` only. Everything else
applies to tests exactly as it does to production code.

## Step 2 — `exhaustive` is the one worth understanding

`commandName`, `direction` and `Environment` are defined types rather than
`string` for one reason: adding a value should break every switch that ignores
it.

That only works with:

```yaml
exhaustive:
  default-signifies-exhaustive: false
```

With `true`, a `default:` arm satisfies the linter and the guarantee is gone.
This was set to `true` here and nobody noticed until a new `commandName` was
added and lint reported **0 issues**. It is now `false`, and the same
experiment reports:

```
cmd/brain/main.go:58:2: missing cases in switch of type main.commandName:
  main.commandWorker (exhaustive)
```

Keep the `default:` arm anyway. It is unreachable by construction and returns
`fmt.Errorf("unhandled command %q", …)`, which is what turns a future mistake
into a clear error rather than silence — and it is covered by a test.

## Step 3 — when a nolint is acceptable

Rarely, and never bare:

```go
//nolint:gosec // G404: this is a test fixture, not a token
```

Rules:

- name the specific linter, never bare `//nolint`
- give the reason after `//`, and make it a reason rather than a restatement
- put it on the narrowest possible line

If the same suppression appears three times, it belongs in `.golangci.yml`
`exclusions` with a comment, not scattered.

**A nolint on one linter does not cover the others on that line.** `w.Write`
returns `(int, error)`, so dropping the assignment to satisfy `gosec` trades a
G705 for an `errcheck` failure. Suppress the one that fires and keep the code
the others want:

```go
	_, _ = w.Write(body) //nolint:gosec // G705: mitigated by the headers above
```

**Where the finding is real but the feature is the point**, the reason has to
say what stands in for the fix. `gosec` G705 flags writing bytes a stranger
supplied — which is exactly what a download route does — so the comment names
the mitigation rather than denying the taint:

```go
	// G705 flags writing bytes a stranger supplied, which is exactly what this
	// route is for, so the answer is the three headers above rather than not
	// writing them. The type is the one sniffed from these bytes at write
	// time, nosniff stops a browser looking past it, and inline is reserved
	// for types that render without executing. gosec cannot see any of that.
	_, _ = w.Write(body) //nolint:gosec // G705: mitigated by the headers above
```

A suppression whose reason is "false positive" is not a reason. If you cannot
name the control that makes it safe, it is not safe yet.

## Step 4 — changing the configuration

Adding or loosening a linter is a decision, not a fix. Before changing
`.golangci.yml`:

1. **Reproduce the thing it should catch.** Write the bad code, run
   `golangci-lint run`, confirm it is silent — as above with `commandWorker`.
2. **Make the change.**
3. **Run the whole suite twice**: once on the current tree to see the change
   costs nothing today, and once with the bad code to confirm it now fires.
4. **Comment the setting** with what it protects, not what it does.

A setting nobody has tested is a setting nobody can rely on.

## Step 5 — verify

```sh
make fmt
make check db=postgres
```

`make lint` alone is not enough — `vet`, `build` and the tests catch different
things, and `make check` is the gate CI uses.
