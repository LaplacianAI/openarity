---
name: add-env-var
description: Add a new environment variable to the brain's config package. Use whenever a new setting must be read from the environment — a new service URL, bind address, credential reference, feature flag, timeout, or enum-valued option. Covers the struct field, validation, redaction and tests so nothing is half-wired.
---

# Add an environment variable to `internal/config`

Every setting the brain reads from the environment lives in one package:
`apps/brain/internal/config`. A new variable is not done until all five steps
below are complete — a field with no validation, no redaction, or no test is a
half-wired setting that fails confusingly in production.

## Conventions that apply

- **Prefix.** Every variable is read with the `OPENARITY_` prefix, applied
  centrally in `load()`. The struct tag omits it: `env:"API_BIND"` reads
  `OPENARITY_API_BIND`.
- **Initialisms keep one case.** `URL` not `Url`, `DSN` not `Dsn`, `API` not
  `Api`, `ID` not `Id`. `revive`'s `var-naming` rule enforces this.
- **Every field gets an `envDefault`.** `RequiredIfNoDef: true` is set, so a
  field without a default becomes mandatory and the process refuses to start
  without it. That is the right behaviour for a genuine secret and the wrong
  one for anything with a sane local value.
- **Defaults must be valid.** `TestValidateAcceptsDefaults` asserts the whole
  default config passes `Validate()`. A default that fails validation means
  nobody can start the process without overriding something.
- **Secrets do not live here.** Config holds a *reference* — a Vault path — not
  a secret value. Values come from `SecretStore` at use time.

## Step 1 — the struct field

In `config.go`, add the field to `Config` under the matching comment group,
creating a new group if none fits.

```go
/* Datastore Configuration */
PostgresDSN string `env:"POSTGRES_DSN" envDefault:"postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable"`
```

Pick the local default to match `deploy/compose.yaml`. If the two disagree,
local development breaks in a way that looks like a code bug.

## Step 2 — a type, if the value is a fixed set

If the setting has a known set of allowed values, do **not** use a bare
`string`. Add a type in `enums.go` with an `UnmarshalText` method, alongside
`Environment` and `LogLevel`:

```go
type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverSQLite   Driver = "sqlite"
)

func (d *Driver) UnmarshalText(text []byte) error {
	switch v := Driver(text); v {
	case DriverPostgres, DriverSQLite:
		*d = v
		return nil
	default:
		return fmt.Errorf("invalid driver: %s", v)
	}
}
```

Two things this buys, both verified: the value is validated before `Validate()`
ever runs, and the `envDefault` is routed through `UnmarshalText` too, so a typo
in your own default is caught at startup.

**Pointer receiver is required.** A value receiver compiles, silently never
runs, and the value is accepted unchecked. `enums_test.go` has a compile-time
assertion that catches this — extend it for the new type.

## Step 3 — validation

In `validate.go`, add a check inside `Validate()`. Use the existing helpers and
pass the **environment variable name**, not the Go field name, so the error
tells an operator what to edit.

| Kind of value | Check |
|---|---|
| bind address, `host:port` | `checkHostPort("NEW_BIND", c.NewBind)` |
| URL you connect to | `checkURL("NEW_URL", c.NewURL, httpSchemes...)` |
| fixed set of values | nothing here — the type validates itself (step 2) |
| relationship between fields | a direct comparison in `Validate()` |

Bind addresses are **not** URLs. `url.Parse("127.0.0.1:21120")` returns an
error, so `checkURL` on a bind address fails on every valid value.

If the value is a URL with a new scheme, add a scheme list to the `var` block
at the top of `validate.go` rather than passing string literals at the call
site.

Both stdlib parsers are lenient, which is why the helpers exist:

- `net.SplitHostPort` only splits on the colon — `"127.0.0.1:abc"` and
  `"127.0.0.1:99999"` both return no error.
- `url.Parse` returns no error for `"nonsense"`, `"http://"` or `""`. Checking
  `err != nil` alone validates nothing.

## Step 4 — `String()`

In `config.go`, add the field to the `String()` format string and its argument
list. **This is the step people forget, and forgetting it leaks credentials.**

- URL-shaped value → wrap in `redactURL(...)`
- anything else → print directly

Wrap every URL even when it has no password today. `Redacted()` is a no-op when
there is nothing to hide, and the field may gain credentials later.

## Step 5 — tests

Update all three, in the file matching the source file:

**`config_test.go`**
- add the field to both the `want` and `got` maps in `TestLoadDefaults`
- if it holds a URL, add a password to it in `TestStringRedactsPasswords` and
  assert the password does not appear

**`validate_test.go`**
- add a rejection case to `TestValidateRejectsWrongSchemePerField` or
  `TestValidateRejectsBadBinds`

**`enums_test.go`** (only if step 2 applied)
- add the type to the `encoding.TextUnmarshaler` compile-time assertion block
- add accept-known and reject-unknown tests, following the `LogLevel` pair

## Step 6 — verify

```sh
cd apps/brain && make check
```

This runs `tidy-check`, `fmt-check`, `vet`, `lint`, `build`, `test -race` and
`govulncheck`. Coverage is enforced separately by `make cover`, currently at
100% — a new field with no test will drop it.

If the variable is meant to be set locally, add it to `.env.development` and
the committed example file too.
