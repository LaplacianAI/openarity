---
name: add-command
description: Add a command or subcommand to oa — a new verb on an existing noun, or a whole new noun. Covers the package, the constructor, registration, unwrapping the generated call, the view type, printing through the printer so every format works, paging, the error vocabulary, and the tests every command owes. Use for every command.
---

# Add a command

Every command is a package under `internal/command/`, built by a
`newXCmd(opts *cli.Options)` constructor, and registered in `cmd/oa/main.go`. A
command is not done until it prints through `opts.Out`, has a view type if it
prints data, and has the tests at the bottom.

## Where it goes

```text
internal/command/<noun>/        one package per noun: whoami, config, context, teams
  <noun>.go                     New(opts) and the verbs
  <noun>_test.go                the tests
internal/cli/options.go         Options — everything a command may reach
internal/cli/response.go        Result, Created, NoContent
internal/cli/page.go            Paging, PrintPage
cmd/oa/main.go                  the command list
```

A new verb on an existing noun is one constructor plus one line in that noun's
`AddCommand`. A new noun is a new package plus one line in `commands()`.

`internal/cli` is the whole surface. A command package imports it, `client`,
`printer` and cobra — nothing else from this module. Needing something more is
a signal to add it to `internal/cli`, not to reach around it.

## Step 1 — the parent

The package's entry point is `New`, so the composition root reads as
`teams.New(opts)`. Verbs are unexported.

```go
func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Create teams and see the ones you belong to",
		Long: "A team owns agents and tools, and membership is what decides who can\n" +
			"see them. Only a super admin can create one.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newCreateCmd(opts),
		newMembersCmd(opts),
	)
	return cmd
}
```

`Short` is a sentence fragment, lowercase after the first word, no full stop —
it appears in a list. `Long` is wrapped by hand at 76 columns because cobra does
not wrap it.

## Step 2 — the command

One constructor per verb. It takes `opts` and returns the command; it never runs
anything at build time.

```go
func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the teams you can see",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListTeamsWithResponse(cmd.Context(),
				&client.ListTeamsParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}
			...
		},
	}

	paging.Flags(cmd)
	return cmd
}
```

- **`RunE`, never `Run`.** An error returned here reaches `main`, is styled
  against stderr and sets the exit code. `Run` cannot fail.
- **`Args` is always set.** `cobra.NoArgs` is a real choice — without it, `oa
  teams list extra` silently ignores the argument.
- **`cmd.Context()`, never `context.Background()`.** It carries the signal
  handler, so Ctrl-C cancels the request in flight.
- **`opts.API(ctx)` for anything authenticated.** It resolves the credential and
  fails before the request when there is none.
- **An argument that names a resource goes through a resolver, not a uuid
  parse.** `cli.ResolveTeam` and `cli.ResolveMember` take a name *or* an id and
  return an id — a uuid is used as given and never looked up, so a script
  passing ids pays nothing.

  Resolve against the narrowest list that can answer. A team goes through
  `GET /teams`, which every member may already read; a member goes through that
  team's own `GET /teams/{id}/members`, never `GET /users` — the directory
  needs `user:read` somewhere, a far larger permission to require for "take
  this person out of my team".

  Better still, do not resolve at all. `oa teams members add` sends the subject
  in the request body and the brain resolves it while adding, so naming
  somebody costs no lookup and no directory permission. Check whether the
  endpoint accepts a name before writing a resolver for it.

  Two things a test owes here: that an id was **not** looked up, and that a
  name nobody has is reported as a name — `"not a uuid"` describes an argument
  the person never intended to type.

## Step 3 — unwrap the call

Three functions, picked by what the endpoint answers. Each takes the generated
call whole and returns the body or a sentence.

```go
page, err := cli.Result(api.ListTeamsWithResponse(ctx, params))          // 200
team, err := cli.Created(api.CreateTeamWithResponse(ctx, body))          // 201
err := cli.NoContent(api.AddTeamMemberWithResponse(ctx, id, body))       // 204
```

Three rather than one because the field the generated code fills is part of the
type: a 201 has `GetJSON201`, and a 204 has no `JSONxxx` field at all, so its
status is the only thing that says it worked. Never hand-write the
`if res.JSONxxx == nil { return cli.APIError(...) }` shape — the point of these
is that the check cannot be forgotten.

## Step 4 — the view

Anything printed as data needs a view type. The struct a command prints is not
the struct it stores, and never the struct a server sent.

```go
type teamView struct {
	ID      string `json:"id" yaml:"id"`
	Name    string `json:"name" yaml:"name"`
	Members int    `json:"members" yaml:"members"`
}
```

- **Both tags, always.** A field with only `json` gets a lowercased Go name in
  yaml, so the two formats disagree — `NextCursor` becomes `nextcursor`.
- **`make([]T, 0, n)`, never `var views []T`.** A nil slice marshals to `null`,
  and `jq length` fails on null — which is exactly what an empty result gives.
- **Never a credential.** `has_token bool`, never the token.
- **Resolve defaults here.** Emitting `"server": ""` makes every consumer learn
  the fallback rule.

A paged endpoint is the exception: hand the items to `cli.PrintPage`, which
prints its own `{items, next_cursor}` envelope. A local list that pages nothing
prints a bare array.

## Step 5 — print it

A paged list:

```go
return cli.PrintPage(opts, cli.Page[client.Team]{
	Items:      page.Items,
	NextCursor: page.NextCursor,
	Empty:      "no teams",
	More:       "oa teams list",
	Row: func(table *printer.Table, team client.Team) {
		table.Row(opts.Styles.Value.Render(team.Name), team.ID.String())
	},
})
```

`More` is the command without its `--cursor` flag; `PrintPage` appends the flag
and the value. Anything else:

```go
return opts.Out.Print(views, printer.Options{
	Table: func(table *printer.Table) {
		for _, view := range views {
			table.Row(opts.Styles.Value.Render(view.Name), view.ID)
		}
	},
})
```

- **Never `fmt.Fprintln(opts.Stdout, ...)` for data.** It bypasses `-o json`
  entirely.
- **A hint goes through `opts.Out.Note`.** It reaches a person and is silent
  under json and yaml, so a consumer never sees prose before the document.
- **Styling happens inside the `Table` callback and nowhere else.** A value
  styled before `Print` puts escape sequences inside a JSON string, and no test
  will catch it: the test writer is a buffer, so `Render` is a no-op there.
- **An empty result still calls `Print`.** Returning early prints nothing, and
  `-o json` must emit `[]`.
- **A field a stranger chose is wrapped in `strconv.QuoteToGraphic`.** Message
  text, sender refs, sender names — anything that reached the brain through a
  channel's hook, which is public by design. A terminal reads `\x1b[2J` as
  "clear the screen", and this is the last place it can be stopped.

  `QuoteToGraphic` rather than stripping the C0 range: it also covers the C1
  controls, invalid UTF-8, and the Cf formatting runes, so a right-to-left
  override cannot make text display in an order it is not stored in. And a
  quoted string cannot contain a newline, so one row stays one row.

  Inside the `Table` callback only — `-o json` must carry what arrived. And
  ask what *chose* the value, not what it travelled through: a ref echoed back
  from argv was copied out of a list of strangers, so it is still theirs.
- **A command that changes something still prints.** `add` and `remove` answer
  204 with no body; they print a small view anyway, because a script needs to
  tell a command that succeeded from one that never ran.

## Step 6 — register it

In `cmd/oa/main.go`:

```go
func commands(opts *cli.Options) []*cobra.Command {
	return []*cobra.Command{
		whoami.New(opts), config.New(opts), cmdcontext.New(opts), teams.New(opts),
	}
}
```

A command that is built and never registered passes every test written about
its body — and `golangci-lint` reports it as `unused`, which is the fastest way
to notice. A subcommand missed in `AddCommand` gets no such warning, so assert
it: `clitest.Execute(t, commands, "<noun>", "--help")` must name every verb.

## Step 7 — the errors

The vocabulary is in `internal/cli/response.go` and is not per-command:

| Status | What the user is told                     |
| ------ | ----------------------------------------- |
| 401    | not authenticated — run `oa login`        |
| 403    | you are not allowed to do that            |
| 404    | not found, or not visible to you          |
| 4xx    | the sentence from the body                |
| 5xx    | the status, plus the body                 |

**403 never says "log in again".** The caller is authenticated and not allowed;
suggesting a re-login is a loop they cannot escape. **404 never confirms
existence** — the brain returns 404 for a team you cannot see, and the CLI must
not undo that by guessing.

Errors are lowercase, no trailing full stop, and name the fix where there is
one: `` `oa context create <name> --server <url>` `` beats "no contexts".

## Step 8 — the tests

Every command owes these. `internal/command/teams/members_test.go` is the
worked example.

1. **It is registered** — `clitest.Execute(t, commands, "<noun>", "--help")`
   names every verb.
2. **It does the thing** — assert the method, the path and the body the stub
   brain received, not just the output.
3. **It refuses bad input** — and sent nothing when it did. Assert both; a
   `Seen` with an empty `Method` is the proof.
4. **A failure is a failure** — drive the statuses the endpoint declares. For a
   204 endpoint, include a 200: it must not print a confirmation.
5. **Every format works** — table, json and yaml, driven in a loop.
6. **No format prints a credential** — driven in a loop over all three.

Then mutate: break a guard and confirm a test fails. A test that passes with
the guard removed is worse than no test, because it certifies nothing while
looking like coverage.

```sh
make check
```
