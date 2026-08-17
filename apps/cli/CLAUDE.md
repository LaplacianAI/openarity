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

- **`add-command`** — every new command and subcommand. Covers the package, the
  `newXCmd(opts *cli.Options)` constructor, registering it, unwrapping the
  generated call, the view type, printing through `opts.Out`, paging, and the
  tests. Use it every time; a subcommand that is built and never registered
  passes every test written about its body.
- **`add-setting`** — every new thing the CLI remembers. Covers the `Config`
  field, `Resolve` and its precedence, `config set`/`unset`/`show`, and whether
  it belongs at the top level or inside a context. Use it every time; a setting
  wired into three of the four places is the normal failure.
- **`handle-a-credential`** — anything that reads, writes, renews, prints or
  moves a login: a store, `oa login`, a context lifecycle operation, a new OAuth
  grant. Covers the keychain size limit and its fallback, which refresh failures
  are fatal, and the four tests. Use it every time; every failure in this area is
  silent — a leak nothing prints, a logout nobody asked for, a renewal that works
  exactly once.
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
    main.go            main(), the command list, and the error printer
  internal/cli/
    root.go            the root command and its persistent flags
    options.go         Options — the whole surface a command may reach
    response.go        Result, Created, NoContent: a call becomes a value
    page.go            Paging and PrintPage: a page becomes a table
    lookup.go          ResolveTeam, ResolveMember: a name becomes an id
  internal/command/
    whoami/            oa whoami
    config/            oa config show|set|unset|path
    context/           oa context list|use|create|rename|delete
    teams/             oa teams list|create, oa teams members list|add|remove
    users/             oa users list — who has logged in, and their id
    login/             oa login — the device flow, start to stored
    logout/            oa logout — discard this context's credential
  internal/theme/      what a theme is — one Parse, zero dependencies
  internal/output/     what a format is
    printer/           how a value is rendered: json, yaml, table
  internal/config/     the config file, and resolving a setting to a value
  internal/credential/ what a login is — a struct, three predicates, one Store
    store/             file.go, keyring.go, and Open, which picks
  internal/auth/       the OIDC provider: discovery, device.go, refresh.go
  internal/ui/         colours, and whether the writer is a terminal
  internal/client/     generated from the brain's spec — never edited
  internal/clitest/    an isolated config directory and a stub brain
  Makefile             build and code quality targets
  .golangci.yml        linters and formatters
```

`internal/credential` imports `time` and nothing else. It is the vocabulary
every other package agrees on, so it cannot import any of them: `store` depends
on it, `config` embeds it in a `Resolve` input, `cli` holds a `Store`. Put a
keychain call in there and `config` starts linking a C library.

`cmd/oa` is the composition root and the only place that knows every command.
One package per command, so `ls internal/command/` names them. `internal/cli`
is what a command may reach and the whole of it; `internal/output` never learns
what a context is; `internal/theme` imports nothing but `strings`.

`clitest` takes the command list as a parameter rather than importing the
packages that build it — every command package's test imports `clitest`, so the
other direction is a cycle. The cost is that it cannot prove a command is
registered on the real root; `TestEveryCommandIsRegistered` in `cmd/oa` is what
covers that.

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
- **A generated call is unwrapped in one line, never six.** `cli.Result`,
  `cli.Created` and `cli.NoContent` take the call whole —
  `page, err := cli.Result(api.ListTeamsWithResponse(ctx, params))` — and hand
  back the body or a sentence. Three functions because the field the generated
  code fills is part of the type: a 201 has `GetJSON201`, and a 204 has no
  `JSONxxx` field at all, so its status is the only thing that says it worked.
- **A list command owns its rows and nothing else.** `cli.Paging` declares
  `--limit` and `--cursor`; `cli.PrintPage` prints the envelope, the note when
  it is empty, and the hint that names the next page. What is left is `Row`.
  A limit of zero is never sent — the brain refuses it, so the flag's own
  default has to mean unspecified.
- **`PrintPage` prints its own envelope type, not the client's.** The generated
  page types carry no yaml tags and `yaml.v3` lowercases a field name, so
  printing them directly spells it `nextcursor` — one name in json and another
  in yaml for the same field, and differently again per collection.
- **Every command prints through `opts.Out`, never `fmt.Fprintln(opts.Stdout)`.**
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
- **An argument that takes an id takes a name too, and a uuid is never looked
  up.** `cli.ResolveTeam` and `cli.ResolveMember` parse first and return an id
  unchanged, so existing scripts pay nothing and a name costs one request.
- **Resolve against the narrowest list that can answer.** A team resolves
  through `GET /teams`, which every member may read. A member resolves through
  that team's own `GET /teams/{id}/members`, not `GET /users` — the directory
  needs `membership:write` somewhere, which is a far larger permission to
  require for "take this person out of my team".
- **Adding somebody resolves nothing.** The subject goes into the request body
  and the brain resolves it while adding, so `oa teams members add` needs no
  directory permission at all. `ResolveUser` existed for one afternoon and was
  deleted when the API grew `subject` — if it comes back, something has been
  designed the wrong way round.
- **An exported identifier that nothing calls is invisible, and coverage is
  what finds it.** `unused` assumes an exported function has a caller in
  another package, so it stays silent. `cli.ParseUUID` outlived its last caller
  by a commit and was found at 0.0% in a per-function coverage report — nothing
  else in the gate said a word. Read the report per function occasionally, not
  just the total; a lone 0.0% is usually dead code rather than an untested
  branch.
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
make check      # everything CI runs: tidy, generate, format, vet, lint, build, cover, vuln
make cover      # coverage, fails below 70%
make generate   # regenerate the client from the brain's spec
make install    # put oa on your PATH, at $(go env GOPATH)/bin
make fmt        # apply gofumpt and fix import order
make tools      # reinstall tooling — rerun after a Go upgrade
```

`make check` is the real gate; run it before saying anything is done.

**Coverage excludes `internal/client` and `internal/clitest`.** Neither is this
module's own code: the generated client is two thousand lines nobody wrote, so
counting it measures oapi-codegen (71.3% with it, 86.3% without), and `clitest`
is the test harness, well covered enough that counting it flatters the total.

## Decisions worth not relitigating

- **This is gcloud, not a chat client.** Contexts, not a single `server` field.
  A credential is only valid for the brain that issued it, so both are keyed by
  the context name and every lifecycle operation moves the pair —
  `oa context rename` renames the credential too, `delete` deletes it.
- **A credential is not a setting.** `config.yaml` is meant to be readable,
  synced and pasted into an issue; a token in it is a leak waiting for the first
  bug report. `Context` holds `{Server string}` and nothing else, and the login
  lives in `internal/credential/store`. This split was made *after* the token
  sat in the config file for a while — do not put it back.
- **Keychain first, file underneath, and the fallback writes to exactly one.**
  `store.Open` returns a `fallback` when a keychain probe succeeds. `Get` reads
  the keychain and falls through to the file; `Set` writes one and **deletes
  from the other**. Without that delete a token too large for the keychain would
  be written to the file while a stale one stayed in the keychain, and the read
  order would keep serving the stale one — a login that appears to succeed and
  changes nothing.
- **The keychain has a size limit, and it is smaller than it looks.** Measured,
  not assumed: macOS caps the whole `security` command line at 4096 bytes and
  the secret is hex-encoded, so about 3009 bytes survive; Windows is 2560. A
  token carrying twenty group claims passes here and fails on somebody else's
  laptop, which is why `ErrTooBig` falls back rather than failing. No test can
  reach the real cap — `keyring.MockInit()` accepts 16KB — so the limit is a
  constant with a comment, not something a test discovers.
- **Renewal reuses the precedence `Resolve` already computed.**
  `renewIfExpired` returns early unless `Settings.Token.Source ==
  Credentials.Location()`. One comparison, and it means the rule for *which*
  credential gets renewed can never drift from the rule for which one gets sent.
  A separate `if flag == "" && env == ""` chain in `cli` would be that drift.
- **Only `invalid_grant` discards a stored login.** `auth.ErrRefreshRejected`
  exists so `renewIfExpired` knows which failures are fatal. A 500 or
  `temporarily_unavailable` is a provider having a bad minute; treating it as a
  dead login would log out everyone the moment authentik restarted, and the two
  bugs are indistinguishable from the outside.
- **A provider that sends no `refresh_token` back is keeping the old one.**
  Rotation is optional in OAuth. Storing what came back would give a login that
  renews exactly once and then dies — which looks identical to a rotating
  provider whose old token was not replaced, and needs the opposite fix.
- **`oa login` checks there is a context before it contacts anyone.**
  Discovering there is nowhere to store a credential *after* a person has
  approved in a browser is the worst possible place to fail.
- **The prompt goes to `opts.Stderr`, not through `opts.Out`.** `Note` is
  silent under json and yaml, and a login whose code is only visible in table
  mode is a login that cannot be completed with `-o json`.
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
