---
name: add-setting
description: Add a setting the CLI remembers — an address, a preference, a reference to something else. Covers whether it belongs at the top level or inside a context, the Config field, precedence in Resolve, the config set/unset/show wiring, and the tests. Use whenever a new value must survive between commands. A credential is not a setting — see handle-a-credential.
---

# Add a setting

Everything the CLI remembers lives in one file, written only through `oa config`
and `oa context`. A setting is not done until every step below is complete — one
wired into four of the five places is a value that saves and never takes effect,
with nothing saying why.

## Step 0 — is it a setting at all?

**A secret is not a setting.** `config.yaml` is meant to be readable, synced
between machines and pasted into an issue. Anything whose value must not be
printed belongs in `internal/credential/store`, keyed by context — read
`handle-a-credential` instead. `token` used to live in `Context` and was moved
out for exactly this reason.

## Step 1 — top level, or inside a context?

This is the only decision that is expensive to change later.

| Ask                                                    | Then          |
| ------------------------------------------------------ | ------------- |
| Is it a property of a particular brain?                 | inside `Context` |
| Would switching brains sensibly change it?              | inside `Context` |
| Is it how *you* like to work, regardless of which brain?| top level     |

`server` is per-context, and is currently the whole of `Context`. `theme` and
`output` are top level — switching context must not silently put you back on a
table mid-script.

## Step 2 — the field

`internal/config/config.go`:

```go
type Config struct {
	Current  string             `yaml:"current,omitempty"`
	Contexts map[string]Context `yaml:"contexts,omitempty"`
	Theme    string             `yaml:"theme,omitempty"`
	Output   string             `yaml:"output,omitempty"`
}
```

**`string`, not the typed enum**, even when the value is one. `config` stores;
it does not interpret. Storing a `theme.Theme` would mean a second parser here,
and an unrecognised value in the file would become a load error rather than
something `oa config show` can display verbatim.

**`omitempty` always** — an unset setting should not appear in the file at all.

## Step 3 — resolve it

`internal/config/resolve.go`. Every setting resolves the same way, and the
order never varies: **flag, environment, file, built-in.**

```go
		Output: pick("output", string(DefaultOutput), "default",
			candidate{outputFlag, "--output"},
			candidate{env("OPENARITY_OUTPUT"), "OPENARITY_OUTPUT"},
			candidate{saved.Output, path},
		),
```

- Add the field to `Settings`. A setting missing there cannot appear in
  `oa config show`, which is the command someone runs to find out why their
  value did not take.
- Omit the flag candidate if there is no flag — `theme` has none, because a
  theme is set once and an output format changes per command.
- The default constant goes in the `const` block with `DefaultTheme`. Point it
  at the owning package's default (`output.Default`), never a bare string.
- **Never put a credential's value in a `Setting`.** `token` reports
  `set (N characters)` and names where it was found — a keychain, a file, a
  flag, `OPENARITY_TOKEN`. See the `token` function.

Its position in the `Settings` struct is the order `oa config show` prints, and
the token goes last.

`Resolve` takes the credential it should describe as an `Input` field rather
than reading a store itself — `config` must not learn what a keychain is. That
same `Token.Source` is what `renewIfExpired` compares against, so the rule for
which credential gets renewed cannot drift from the rule for which gets sent.

## Step 4 — make it settable

`internal/command/config/config.go`, three places, all switches:

```go
			case "output":
				chosen, ok := output.Parse(value)
				if !ok {
					return fmt.Errorf("%q is not an output format — use one of %s", value, output.Names())
				}
				saved.Output = string(chosen)
```

```go
			case "output":
				saved.Output = ""
```

```go
	case "output":
		overriding = o.settings.Output
```

- **`set` validates and stores the parsed value.** Storing the raw string lets
  `JSON` reach the file uppercased; storing an unvalidated typo lets it sit
  there being silently ignored on every run.
- **The `default:` arm lists the valid keys** — update it, and the `Long` text
  above it.
- **`confirm` warns when an environment variable overrides what was just
  written.** Without the new case, `oa config set` reports success on a value
  that will not take effect in that shell.
- **A credential is never settable here.** `oa config set token` is refused on
  purpose: a secret typed as a shell argument lands in history. `oa login` is
  how one arrives. `unset token` is allowed and reaches the credential store,
  not the config file — it is `oa logout` under the name someone will guess.

For a per-context setting, write through `updateActive` rather than assigning —
it creates the default context when none exists, so the first command after a
fresh install works.

## Step 5 — the enum, if it is one

A fixed set of strings gets its own leaf package with exactly one `Parse`, like
`internal/theme` and `internal/output`. See `add-output-format`.

Never a second parser. `config` stores the value and something else renders it;
two parsers is how "dark" starts meaning different things in two places.

## Step 6 — the tests

In `internal/config/resolve_test.go`:

1. **Precedence** — all four sources, as a table, asserting which wins.
2. **The default** — the built-in, and that its `Source` says `default`.
3. **The source is named** — for the flag, the variable and the file.
4. **An unknown value is reported verbatim** — not normalised. The typo has to
   be visible in the one command someone runs to find it.
5. **Scope** — resolve twice with a different `Current` and assert the value
   did or did not change, matching the step 0 decision.

In `internal/command/config/config_test.go`:

6. **`set` writes it**, normalised.
7. **`set` refuses a bad value**, and wrote nothing when it did.
8. **`unset` removes only its own key.**
9. **`show` lists it**, in every format.

Then mutate each guard and confirm the test fails — swapping two candidates in
`Resolve` is the mutation that catches a precedence test that is only checking
the happy path.

```sh
make check
```
