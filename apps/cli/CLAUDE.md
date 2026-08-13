# oa

The Openarity CLI. One Go module, one binary, one job: talk to a brain over its
HTTP API. The API contract lives in `apps/brain/api/openapi.yaml` and the client
is generated from it — this module never guesses what an endpoint looks like.

## Working agreement

**The user writes the production code. Claude writes the tests and tries to
break it, explains, and reviews.**

Same as the brain, and it holds even when an instruction sounds like the
opposite. "Make the change yourself, be like me" is a *style* instruction —
sparse comments, only where the why is not obvious — not permission to write
production files.

- One file per reply, then stop and wait. The user reads it, raises doubts, and
  changes the design *in that file* before the next one exists. Naming the
  remaining files as a checklist is fine; handing over their contents is not.
- Never hand over signatures, skeletons or `// ...` placeholders. Every function
  given is complete: imports, body, error paths.
- Once a file exists, give only the edits, each anchored and each with a
  sentence on why. A whole file is only right the first time it is created.
- Claude writes test files to disk directly — those are Claude's own work
  product.
- Verify library behaviour with a throwaway probe rather than asserting it from
  memory. `url.Parse` accepts `not a url at all` without error; `lipgloss.Width`
  and `len` disagree by 15 bytes on a styled five-character string; `yaml.v3`
  panics rather than returning an error on an unsupported type.

## Skills

- **`add-command`** — every new command and subcommand. Covers the file, the
  `newXCmd(opts *options)` constructor, registering it, the view type, printing
  through `opts.out`, and the tests. Use it every time; a command that is built
  and never registered passes every test written about its body.
- **`add-setting`** — every new thing the CLI remembers. Covers the `Config`
  field, `Resolve` and its precedence, `config set`/`unset`/`show`, and whether
  it belongs at the top level or inside a context. Use it every time; a setting
  wired into three of the four places is the normal failure.
- **`add-output-format`** — a fourth format alongside table, json and yaml.
  Covers the `Format` constant, `Parse`, the printer type, and the two tests
  that stop it being silently wrong.
- **`regenerate-client`** — after the brain's spec changes. Covers `make
  generate`, what oapi-codegen does and does not support, and why the generated
  file is committed.
- **`write-tests`** — every test. Covers the `execute`/`isolate`/`seed` helpers,
  when `t.Parallel()` is allowed, and step 7: mutate the guard and confirm the
  test fails. A test that passes with the code broken is worse than no test.
- **`fix-lint`** — any golangci-lint or gofumpt failure. Same linter set as the
  brain, and the fix is almost never a `nolint`.

## Layout

```text
apps/cli/
  cmd/oa/
    main.go            main(), and the error printer
    root.go            the root command, options, load(), api()
    config.go          oa config show|set|unset|path
    context.go         oa context list|use|create|rename|delete
    whoami.go          oa whoami
    views.go           the shapes commands print
    response.go        turning an HTTP response into a sentence
  internal/theme/      what a theme is — one Parse, zero dependencies
  internal/output/     what a format is
    printer/           how a value is rendered: json, yaml, table
  internal/config/     the config file, and resolving a setting to a value
  internal/auth/       which credential to send, and to whom
  internal/ui/         colours, and whether the writer is a terminal
  internal/client/     generated from the brain's spec — never edited
  Makefile             build and code quality targets
  .golangci.yml        linters and formatters
```

`cmd/oa` is the composition root and the only place that knows every
dependency. `internal/output` never learns what a context is; `internal/theme`
imports nothing but `strings`.

Each app in the monorepo is a separate Go module, so `apps/brain/internal/` is
unreachable from here by construction. The only thing crossing the boundary is
`api/openapi.yaml`, and it is a spec, not code.

## Conventions

- **Initialisms keep one case**: `URL`, `API`, `ID`, `JSON`, `YAML`. Set by
  `revive`'s `var-naming` and by oapi-codegen's `ToCamelCaseWithInitialisms`
  normaliser, which is why the generated client has `UserID` and not `UserId`.
- **A fixed set of strings is a defined type with one `Parse`.** `theme.Theme`
  and `output.Format` exist so `exhaustive` fails the build when a value is
  added and a switch forgets it. That depends on
  `default-signifies-exhaustive: false`; keep the `default:` arm anyway, and
  never write `default:` where a `case` belongs.
- **The package that owns a type owns its parsing.** `config` stores a theme
  and `ui` renders one; neither interprets it. A second parser is how "dark"
  starts meaning different things in two places.
- **An unrecognised value is reported verbatim, not normalised.** `oa config
  show` printing `solarized` is how someone finds their typo. Normalising it to
  `auto` hides the mistake in the one command they ran to find it.
- **A bad output format is a hard error; a bad theme is not.** A wrong colour
  still shows the data. A table written into a file that expected JSON does not.
- **Every command prints through `opts.out`, never `fmt.Fprintln(opts.stdout)`.**
  The exception is a hint, which goes through `Note` — it reaches a person and
  is silent under json and yaml, so a consumer never sees prose before the
  document.
- **A list prints an empty array, not nothing.** Build view slices with
  `make([]T, 0, n)`: a nil slice marshals to `null`, and `jq length` fails on
  null. That is exactly what a fresh install would produce.
- **Table cells are padded by display width, never by `tabwriter`.**
  `text/tabwriter` counts bytes, so a styled cell is measured with its escape
  sequences and every other row is padded to match — 26 columns out of line, and
  only ever on a terminal, so no test and no pipe will show it. `printer.Table`
  uses `lipgloss.Width`.
- **Styling happens inside the table callback and nowhere else.** A value styled
  before it reaches `Print` puts escape sequences inside a JSON string. No test
  catches this: the test writer is a `bytes.Buffer`, lipgloss detects no
  terminal, and `Render` is a no-op.
- **Take the writer as a parameter.** `ui.New(w, theme)` builds styles against
  the writer they print to, so errors are styled for stderr and output for
  stdout. Anything writing to `os.Stdout` directly cannot be observed by a test.
- **Never print a credential.** Not truncated, not masked, not in an error.
  `config.Resolve` never puts a token value in a `Setting` — it reports
  `set (N characters)` — so there is nothing to redact further up.
- **`os.Exit` appears only in `main`, and `main` holds no defers.** Same reason
  as the brain: `os.Exit` skips deferred functions.
- **Tests mirror source files**, and a test that needs a config file gets its
  own directory through `isolate(t)`.
- **Config writes are atomic.** `os.WriteFile` opens with `O_TRUNC`, so a
  concurrent reader sees an empty file and YAML parses that happily into a zero
  config. Temp file, then rename.

## Commands

```sh
make            # list targets
make check      # everything CI runs: tidy, generate, format, vet, lint, build, test
make cover      # coverage, fails below 70%
make generate   # regenerate the client from the brain's spec
make install    # put oa on your PATH, at $(go env GOPATH)/bin
make fmt        # apply gofumpt and fix import order
make tools      # reinstall tooling — rerun after a Go upgrade
```

`make check` is the real gate; run it before saying anything is done.

**Coverage excludes `internal/client`.** It is roughly two thousand generated
lines nobody wrote, so counting it measures oapi-codegen — 34.6% with it, 83.8%
without.

## Decisions worth not relitigating

- **This is gcloud, not a chat client.** Contexts, not a single `server` field.
  A credential is only valid for the brain that issued it, so the token lives
  *inside* the context and travels with it.
- **Output is a top-level setting, not per-context.** It is how you like to read
  output, not a property of a brain. Switching context must not silently put you
  back on a table mid-script.
- **The development token is only ever sent to a loopback address.** The client
  decides this, never the server. Letting `GET /auth/config` be authoritative
  means any host reached by a typo can claim to be in development and collect
  `OPENARITY_DEV_TOKEN` from the shell. `url.Parse` is lenient enough to accept
  nonsense, so the check tests `parsed.Host != ""` and then `net.ParseIP`.
- **`ErrNoCredential` means "you need to log in", nothing else.** A network
  failure and a missing endpoint return plain errors — telling someone to run
  `oa login` because their VPN is down sends them down the wrong path.
- **403 never says "log in again".** The caller is authenticated and not
  allowed; suggesting a re-login is a loop they cannot escape.
- **The printers are a strategy, not a switch.** Three types behind one
  interface, picked by a factory, sharing `base` by embedding. Go has no virtual
  dispatch, so `base` must never call a method a child overrides — it does not,
  which is why the shape is safe.
- **The second argument to `Print` is a struct, not functional options.**
  `printer.Options{Table: ...}` — named fields, zero value works, and tomorrow's
  yaml formatting is one more field. An `Option func(*settings)` bag with an
  `apply` helper is machinery for three formats.
- **The generated client is committed.** CI regenerates it and fails if the
  result differs, which is what catches a spec change landing in the brain
  without the client being rebuilt.
- **oapi-codegen v2.8.0 handles OpenAPI 3.1.** Verified with a probe, not
  assumed. `client: true` generates both the raw `ClientInterface` and the typed
  `ClientWithResponses`; commands use the typed one.
