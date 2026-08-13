---
name: add-command
description: Add a command or subcommand to oa — a new verb on an existing noun, or a whole new noun. Covers the file, the constructor, registration, the view type, printing through the printer so every format works, the error vocabulary, and the tests every command owes. Use for every command.
---

# Add a command

Every command lives in `cmd/oa/`, is built by a `newXCmd(opts *options)`
constructor, and is registered in `newRootCmd`. A command is not done until it
prints through `opts.out`, has a view type if it prints data, and has the tests
at the bottom.

## Where it goes

```text
cmd/oa/root.go        newRootCmd, options, load(), api() — the composition root
cmd/oa/<noun>.go      one file per noun: config, context, teams
cmd/oa/views.go       the shapes commands print
cmd/oa/response.go    turning an HTTP failure into a sentence
```

A new verb on an existing noun is one constructor plus one line in that noun's
`AddCommand`. A new noun is a new file plus one line in `newRootCmd`.

## Step 1 — the parent, if the noun is new

```go
func newTeamsCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Create teams and manage who is in them",
		Long: "A team owns agents and tools. Membership is what decides who can\n" +
			"see them.",
	}

	cmd.AddCommand(
		newTeamsListCmd(opts),
		newTeamsCreateCmd(opts),
	)
	return cmd
}
```

`Short` is a sentence fragment, lowercase after the first word, no full stop —
it appears in a list. `Long` is wrapped by hand at 76 columns because cobra does
not wrap it.

## Step 2 — the command

One constructor per verb. It takes `opts` and returns the command; it never
runs anything at build time.

```go
func newTeamsListCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the teams you can see",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := opts.api(cmd.Context())
			if err != nil {
				return err
			}

			res, err := api.ListTeamsWithResponse(cmd.Context(), nil)
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body)
			}
			...
		},
	}
}
```

- **`RunE`, never `Run`.** An error returned here reaches `main`, is styled
  against stderr and sets the exit code. `Run` cannot fail.
- **`Args` is always set.** `cobra.NoArgs` is a real choice — without it, `oa
  teams list extra` silently ignores the argument.
- **`cmd.Context()`, never `context.Background()`.** It carries the signal
  handler, so Ctrl-C cancels the request in flight.
- **`opts.api(ctx)` for anything authenticated**, `opts.bare` for anything that
  is not. `api()` resolves the credential and fails before the request when
  there is none.

## Step 3 — the view

Anything printed as data needs a view type in `views.go`. The struct a command
prints is not the struct it stores, and never the struct a server sent.

```go
type teamView struct {
	ID      string `json:"id" yaml:"id"`
	Name    string `json:"name" yaml:"name"`
	Members int    `json:"members" yaml:"members"`
}

func teamViews(items []client.Team) []teamView {
	views := make([]teamView, 0, len(items))
	for _, one := range items {
		views = append(views, teamView{...})
	}
	return views
}
```

- **Both tags, always.** A field with only `json` gets a lowercased Go name in
  yaml, so the two formats disagree.
- **`make([]T, 0, n)`, never `var views []T`.** A nil slice marshals to `null`,
  and `jq length` fails on null — which is exactly what an empty result gives.
- **Never a credential.** `has_token bool`, never the token.
- **Resolve defaults here.** Emitting `"server": ""` makes every consumer learn
  the fallback rule.

An endpoint that returns a page envelope is the exception: print the response
as it arrived, so `items` and `next_cursor` survive. A local list that pages
nothing prints a bare array.

## Step 4 — print it

```go
return opts.out.Print(views, printer.Options{
	Table: func(table *printer.Table) {
		for _, view := range views {
			table.Row(opts.styles.Value.Render(view.Name), view.ID)
		}
	},
})
```

- **Never `fmt.Fprintln(opts.stdout, ...)` for data.** It bypasses `-o json`
  entirely.
- **A hint goes through `opts.out.Note`.** It reaches a person and is silent
  under json and yaml, so a consumer never sees prose before the document.
- **Styling happens inside the `Table` callback and nowhere else.** A value
  styled before `Print` puts escape sequences inside a JSON string, and no test
  will catch it: the test writer is a buffer, so `Render` is a no-op there.
- **An empty result still calls `Print`.** Returning early prints nothing, and
  `-o json` must emit `[]`.

## Step 5 — register it

```go
	root.AddCommand(
		newWhoamiCmd(opts),
		newConfigCmd(opts),
		newContextCmd(opts),
		newTeamsCmd(opts),
	)
```

A command that is built and never registered passes every test written about
its body. Only a test that drives the root catches it, which is why
`execute(t, ...)` goes through `newRootCmd`.

## Step 6 — the errors

The vocabulary is in `response.go` and is not per-command:

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

## Step 7 — the tests

Every command owes these. `cmd/oa/context_test.go` is the worked example.

1. **It is registered** — `execute(t, "<noun>", "--help")` names every verb.
2. **It does the thing** — the file or the request changed as expected.
3. **It refuses bad input** — and changed nothing when it did. Assert both.
4. **Every format works** — table, json and yaml, driven in a loop.
5. **No format prints a credential** — driven in a loop over all three.

Then mutate: break a guard and confirm a test fails. A test that passes with
the guard removed is worse than no test, because it certifies nothing while
looking like coverage.

```sh
make check
```
