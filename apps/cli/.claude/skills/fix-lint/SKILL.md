---
name: fix-lint
description: Fix a golangci-lint or gofumpt failure in the CLI, or decide whether to change the lint configuration. Covers what is enabled and why, the linters that actually fire here and the correct fix for each, the exclusions and what they cost, when a nolint is acceptable, and how to verify a configuration change catches what it claims to.
---

# Fix a lint failure

```sh
cd apps/cli
make fmt      # apply formatting — never fix gofumpt or import order by hand
make lint     # golangci-lint run
```

`.golangci.yml` is **deliberately the same set as `apps/brain`**. Two modules
with two standards means every contributor learns which directory they are in
before they can tell whether something is a real finding.

## The formatters

`gofumpt` with `extra-rules`, and `gci` for import order. Never fix either by
hand — `make fmt` applies both.

Import groups, in order: standard library, third party, then
`github.com/LaplacianAI/openarity`. A blank line between each.

The one that surprises people: **gofumpt groups adjacent single `const`
declarations.** Two `const X = ...` lines in a row become a `const ( ... )`
block, and `fmt-check` fails until they are.

## The linters that actually fire here

**`exhaustive`** — a switch over `theme.Theme` or `output.Format` missing a
case. The fix is the case, never a `default:`.

`default-signifies-exhaustive` is `false` on purpose. A `default:` arm does
**not** excuse a missing case, which is the entire point: adding a value to an
enum must break every switch that ignores it. Keep the `default:` arm anyway
where it returns a clear fallback, but never write one *instead of* a case.

**`gosec` G304** — a file read with a non-constant path. `config.Load` carries
the one `#nosec` in the module:

```go
	// #nosec G304 -- the path is built by Path() from the user's own config
	// directory, not from input. Nothing reaches it from a flag or a server.
	data, err := os.ReadFile(path)
```

A `#nosec` **must** carry the reason after `--`. One without is a silenced
finding nobody can review.

**`errcheck`** — an ignored error. Assign to `_` only where ignoring is the
intent and it is obvious why: `defer func() { _ = os.Remove(temp.Name()) }()`.
In tests, a setup command whose failure would invalidate the assertion goes
through `seed(t, ...)`, which fails the test.

**`revive` var-naming** — initialisms keep one case. `URL`, `API`, `ID`,
`JSON`, `YAML`. Unexported names starting with one go all-lowercase:
`urlParser`, not `uRLParser`.

**`unparam`** — a parameter that is always the same value, or a result never
used. Usually a real signal that the signature is wider than the need.

**`noctx`** — an HTTP request without a context. Every request carries
`cmd.Context()`.

**`bodyclose`** — an unclosed response body. Rare here, because commands use the
generated `WithResponse` variants, which read and close for you.

## The exclusions, and what they cost

Two, both in `.golangci.yml`:

```yaml
      - path: _test\.go
        linters: [gosec, unparam, bodyclose]

      - path: internal/client/client\.gen\.go
        linters: [bodyclose, errcheck, gocritic, gosec, revive, unparam]
```

Tests may take shortcuts production code may not. The generated client is
excluded because a finding there cannot be acted on — the next `make generate`
overwrites the fix.

Neither is a licence to write worse code in those files. `bodyclose` is off in
tests only because a test builds `*http.Response` literals to drive `apiError`,
and those carry no body to close.

## When a nolint is acceptable

Almost never. Before writing one:

1. **Is the linter right?** It usually is. `unparam` pointing at an unused
   parameter is telling you the signature is wrong.
2. **Can the code change instead?** A narrower interface, a smaller function, a
   value that is not ignored.
3. **Is this the generated file or a test?** Then it is already excluded and a
   `nolint` is noise.

If it survives all three, write it with the reason:

```go
//nolint:linter // why, in a sentence someone reviewing can check
```

## Changing the configuration

Adding or removing a linter is a decision, not a fix, and it applies to both
modules or neither — the two files are meant to match.

Verify the change catches what it claims:

```sh
# write code that should fail, run lint, confirm it does
make lint
# revert, confirm it passes
```

This matters more than it sounds. `default-signifies-exhaustive` was `true` in
the brain for three steps, and adding an enum value reported **0 issues** — the
setting looked present and did nothing. A configuration change that has not been
shown to fire is a configuration change that does not work.

```sh
make check
```
