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

## Layout

```text
apps/brain/
  internal/config/     configuration: load, validate, redact
  Makefile             build and code quality targets
  .golangci.yml        linters and formatters
```

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

## Commands

```sh
make            # list targets
make check      # everything CI runs: tidy, fmt, vet, lint, build, test, vuln
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
  raw request body, so nothing may parse it first.
- **Secrets are references.** A row or config field holds a Vault path, never a
  value. Only `internal/secrets` imports a secret backend SDK.
