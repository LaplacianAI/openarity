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
`string`.

**First check whether the stdlib already has the type.** Anything implementing
`encoding.TextUnmarshaler` drops straight into a struct tag with no code at
all. `LogLevel` was a hand-written enum until `slog.Level` turned out to parse
case-insensitively and reject unknown names with a better message than ours —
34 lines deleted. `time.Duration` is the other common one. Write a probe under
the scratchpad and check what it actually accepts before assuming.

Write your own type only when the constraint is yours. `Environment` has no
stdlib equivalent, so it stays. Add it in `enums.go` with an `UnmarshalText`
method, alongside `Environment`:

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

`TestLoadDefaults` ends with a reflective guard that walks `Config`'s fields
and fails on any name missing from the `want` map. The table is hand-written,
so without it a new field is not *wrong*, it is *absent* — the test stays green
while the setting goes unchecked. If that guard fires, add the default; do not
delete the guard.

For a slice field, the map holds a shape rather than a value —
`fmt.Sprintf("%d entries", len(cfg.SuperAdmins))` — because the map is
`map[string]string`.

**`validate_test.go`**
- add a rejection case to `TestValidateRejectsWrongSchemePerField` or
  `TestValidateRejectsBadBinds`

**`enums_test.go`** (only if step 2 applied)
- add the type to the `encoding.TextUnmarshaler` compile-time assertion block
- add accept-known and reject-unknown tests, following the `Environment` set

If the type came from the stdlib instead, do not re-test its parsing — assert
the **wiring** through `load`, as `TestLoadAcceptsAnyCaseLogLevel` does.

## Step 6 — verify

```sh
cd apps/brain && make check db=openarity_test
```

`check` runs `tidy-check`, `generate-check`, `fmt-check`, `vet`, `lint`,
`build`, `cover` and `vuln`. Coverage is inside it, not separate — a new field
with no test fails the gate rather than merely lowering a number.

**Always pass `db=`.** It sets `BRAIN_TEST_POSTGRES_DSN`; without it every
database test skips, coverage drops by roughly 25 points, and the report looks
like a coverage problem that is not one. `make testdb db=openarity_test`
creates the database, once per machine.

If the variable is meant to be set locally, add it to `.env` too.

## Step 7 — prove the test can fail

A test that passes against broken code is worse than no test. For each guard
added, break it and confirm the expected test fails, then restore:

| Break | Should fail |
| --- | --- |
| delete the validation block | the rejection test in `validate_test.go` |
| widen a condition (`if c.X != ""` → `if false`) | the same test |
| couple a check to an unrelated field | the case that check was written for |
| remove the field from `String()`'s arguments | the redaction test |

The third row is the one that catches real mistakes. A check nested inside an
unrelated `if` still passes every test written for the happy path — the
`SUPER_ADMINS` validation was briefly nested inside `if c.OIDCEnabled`, and
only a test that exercised it with OIDC *disabled* noticed.

## Settings that must never be logged

Some fields are secrets with no safe redacted form — a bearer token has no
host or username worth keeping. For those, leave them out of `String()`
entirely rather than redacting them.

Omission still needs a test, because nothing in the code says the omission was
deliberate:

```go
// String() must not grow these fields. If it does, this test says so and
// explains what to do instead.
func TestStringOmitsTheAuthenticationSettings(t *testing.T) {
```

Assert the *token value* is absent, not that some redaction marker is present —
a test asserting `String()` redacts a field it never prints passes vacuously
and proves nothing.
