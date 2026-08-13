---
name: regenerate-client
description: Regenerate internal/client after the brain's OpenAPI spec changes — a new endpoint, a changed response, a renamed field. Covers what oapi-codegen produces, which client shape commands use, why the output is committed, what the generator does not support, and how a spec change lands across two modules in one pull request. Use whenever apps/brain/api/openapi.yaml changes.
---

# Regenerate the client

The CLI never hand-writes a request or a response type. Both come from
`apps/brain/api/openapi.yaml`, so a change there is a compile error here rather
than a bug a user finds.

```sh
cd apps/cli
make generate
```

That runs `oapi-codegen -config oapi-codegen.yaml ../brain/api/openapi.yaml`
and writes `internal/client/client.gen.go`. Commit the result.

## The spec changes first

The order matters, and it is one pull request, not two:

1. Change `apps/brain/api/openapi.yaml` — see the brain's `update-api-spec`
   skill. The spec is hand-written; it is not generated from the routes.
2. `cd apps/brain && make check db=postgres`. A test there fails when the spec
   and the registered routes disagree, so an undocumented endpoint stops here.
3. `cd apps/cli && make generate`.
4. `make check`. A removed or renamed field is a compile error at the call site,
   which is the whole point.
5. Commit both modules together.

Splitting it across two pull requests leaves `main` in a state where CI fails on
the second one for a reason that has nothing to do with it.

## What it generates

`models: true` gives the wire types from `components/schemas`.

`client: true` gives **two** shapes:

| Shape                 | Returns                         | Use it        |
| --------------------- | ------------------------------- | ------------- |
| `ClientInterface`     | `*http.Response`                | almost never  |
| `ClientWithResponses` | a struct with `JSON200`, `Body` | every command |

Commands use the second. The CLI has to tell 403 from 404 to say anything
useful, and the typed variant parses a body per status code:

```go
	res, err := api.ListTeamsWithResponse(ctx, nil)
	if err != nil {
		return err
	}
	if res.JSON200 == nil {
		return apiError(res.HTTPResponse, res.Body)
	}
```

`err` is a transport failure — the request never completed. A non-2xx status is
**not** an error here; `res.JSON200` being nil is how you find out. Treating
`err == nil` as success is the mistake this shape invites.

## Settings that are load-bearing

`oapi-codegen.yaml` is short, and two lines in it are not cosmetic:

- **`name-normalizer: ToCamelCaseWithInitialisms`** — initialisms keep one case,
  so the generated names are `UserID` and `ClientID`. Without it they are
  `UserId` and `ClientId`, which is the one place in the repository that breaks
  `revive`'s `var-naming`, and every call site inherits the wrong spelling.
- **`package: client` / `output: internal/client/client.gen.go`** — one file,
  one package, never edited by hand. `.golangci.yml` excludes it from
  `bodyclose`, `errcheck`, `gocritic`, `gosec`, `revive` and `unparam`, because
  a finding there cannot be acted on: the next `make generate` overwrites it.

## Why the output is committed

So that `make generate-check` can exist. It regenerates and fails if the result
differs from what is committed:

```text
internal/client/client.gen.go is out of date — run 'make generate' and commit the result
```

CI runs it in the `cli` job. That is the check that catches a spec change
landing in the brain without the client being rebuilt — the failure mode this
repository is actually shaped for, because the two modules share nothing else.

It also means a clone builds without the generator installed.

## What the generator will not do for you

- **OpenAPI 3.1 is supported.** v2.8.0 handles it; this was verified with a
  probe rather than assumed. If a construct fails to generate, check the
  construct before blaming the version.
- **It does not invent a client for an undocumented route.** If a command needs
  an endpoint, the endpoint is in the spec first.
- **It does not know about pagination.** A response with `items` and
  `next_cursor` generates as a struct with those fields; looping is the
  command's job.
- **A generated field is not a view.** Do not print the generated struct
  directly — build a view in `views.go`, so the printed shape does not change
  the day the wire shape does. The exception is a page envelope, which is
  printed as it arrived so `items` and `next_cursor` survive.

## After a Go toolchain upgrade

```sh
make tools
```

`oapi-codegen` is installed with `go install` and is compiled against the Go
present at the time. After an upgrade it fails with a version-mismatch error
until reinstalled, and the error does not say that clearly.
