---
name: add-route
description: Add an HTTP route to the brain's API — a new endpoint on an existing domain, or a whole new domain package. Covers where the route lives, the Router, per-package dependencies, the response contract, which status code each failure gets, mapping the route in rbac.json, whether a request body should reference another resource by id or by name, choosing between the API and webhook listeners, and the tests every route owes. Use for every endpoint.
---

# Add a route to the API

Every API route lives in a domain package under `internal/api/`, is mounted by
`internal/server`, and runs behind authentication, user resolution and the
authorisation guard. A route is not done until it is mapped in `rbac.json`, has
a response contract, and has the five tests at the bottom.

**The handler contains no authorisation check.** Which check runs is a row, and
the guard applies it before the handler is reached — a route with no row will
not start. Read the `authorise-a-route` skill for the scopes and the file; this
skill assumes you have chosen one.

## Step 0 — which listener

There are two, and they are not interchangeable. Getting this wrong is not a
style mistake: an API route on the webhook listener answers with no check at
all, and a hook on the API listener rejects every provider on earth for not
carrying a bearer token.

| | API listener | webhook listener |
| --- | --- | --- |
| bind | `OPENARITY_API_BIND` | `OPENARITY_WEBHOOK_BIND` |
| who calls it | `oa`, the dashboard, a script | Slack, Telegram, anything on the internet |
| the question it asks | *who are you, and what may you do* | *did this come from someone holding this channel's secret* |
| how it is mounted | `newRouters` → `server.New(..., routers...)` | `newWebhookRouters` → `Deps.WebhookRouters` |
| `rbac.json` row | required | none — the router is public |
| in `openapi.yaml` | required | no; it is not part of the client contract |
| router | `api.NewRouter(prefix)` | `api.NewPublicRouter(prefix)` |

Everything in the rest of this skill is about the **API** listener. If you are
adding a provider webhook, you are almost certainly not adding a route at all:
`internal/gateway` mounts one per `(provider, suffix)` automatically from the
registry, so an adapter declaring a `Route` is the whole of it. Read
`internal/gateway/CLAUDE.md`.

A webhook router must be public. `server.New` refuses a non-public one by name,
because that listener has no guard to hand `Register` and the alternative is a
nil-pointer panic somewhere less obvious.

**`Router.Handle(method, pattern, h)`** exists for the gateway, whose method
comes from an adapter rather than from a literal. Use `Get`/`Post`/… everywhere
else — a method you can spell at the call site should be spelled there.

## Where it goes

```text
internal/api/router.go            Router: prefixes, verb helpers, Register
internal/api/response.go          WriteJSON — the one shared response function
internal/api/request.go           DecodeJSON — the one shared body reader
internal/api/<domain>/            one package per domain: teams, agents, tools
internal/api/<domain>/<domain>.go handler, New, the handlers
internal/api/<domain>/schema.go   request and response structs — the wire contract
cmd/brain/routers.go              one line per domain, the only assembly point
```

The wire structs live in `schema.go`, in the domain package. Not `dto.go` —
that is a Java term Go code does not use — and not `types.go`, a catch-all that
ends up holding whatever has no home. And never a shared `internal/api/schema`
package: every domain would import one struct grab-bag, and a field added for
agents would surface in the teams response.

A new endpoint on an existing domain is one line in that package's `New` plus a
handler. A new domain is a new directory and one line in `routers.go`.

Do not add a file to `internal/api` itself unless every domain needs it.

## Step 1 — the package

```go
package teams

// Store is the slice of the database this package uses, declared here rather
// than taking *store.Store. Every route test then runs without Postgres, and a
// query this package never calls cannot appear in it by accident.
type Store interface {
	CreateTeam(ctx context.Context, name string) (db.Team, error)
	GetTeam(ctx context.Context, id uuid.UUID) (db.Team, error)
}

// handler is unexported and holds exactly what this package needs. A missing
// dependency is then a compile error rather than a nil at request time.
type handler struct {
	logger *slog.Logger
	store  Store
}

func New(logger *slog.Logger, s Store) *api.Router {
	h := &handler{logger: logger, store: s}

	r := api.NewRouter("/teams")
	r.Post("", h.create)
	r.Get("", h.list)
	r.Get("/{id}", h.get)
	return r
}
```

Most domain packages take **no authorizer at all** — `users` does not import
`internal/authz`. Take one only when a handler decides *what to return* rather
than *whether to run*: `teams` declares a one-method `SuperAdmins` interface
because `GET /teams` shows every team to a super admin and only your own to
everyone else. That is a content decision, not an access check.

Name such an interface for what it answers, not `Authorizer`. `api.Authorizer`
is the guard's, and it is the only one that decides access — a second type
called `Authorizer` makes every mention of the word ambiguous.

- **`New` returns `*api.Router`**, which satisfies `server.Router`. Nothing else
  is exported.
- **The prefix lives in `NewRouter`**, and patterns are written relative to it.
  `r.Get("/{id}")` under `/teams` is `GET /teams/{id}`, and `r.PathValue("id")`
  works normally.
- **Do not share a `handler` struct between packages.** They look alike for the
  first two domains and diverge immediately. A shared struct holding every
  dependency is a service locator, and it hides what a package actually uses.
- **Declare dependencies as interfaces in the consuming package**, listing only
  the methods used. `*store.Store` and `*authz.Authorizer` satisfy them without
  changing, `cmd/brain` still passes the concrete types, and the route tests
  need no database.

## Step 2 — the response contract

Never serialise `auth.User`, a `db.` struct, or anything else internal. Declare
the wire shape in the package:

```go
type response struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Teams []team    `json:"teams"`
}
```

- **Internal types are free to change; this one is not.** A column rename must
  not reach a client.
- **`omitempty` for a value that is genuinely absent**, never for a collection.
  An empty list is an answer — "we looked, there are none" — and dropping the
  key makes it indistinguishable from an old server.
- **Build slices with `make(..., len(src))`** so they serialise as `[]`, not
  `null`.
- **Do not publish internal keys** that are not yet a contract. `whoami` omits
  the user id on purpose: publishing it invites clients to put it in URLs.

Write it with `api.WriteJSON(w, h.logger, http.StatusOK, resp)`.

## Step 2c — a route that returns bytes

Some routes answer with a file rather than JSON. The response contract is then
the *headers*, and every one of them is a decision:

```go
	w.Header().Set("Content-Type", row.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition(row.MediaType, row.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, no-store")

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
```

- **Set every header before the first `Write`.** `net/http` flushes the header
  block on the first byte and silently discards anything set afterwards — no
  error, no panic, no log. Worse, `http.DetectContentType` fills in a
  `Content-Type` for you, so the response still looks plausible while the type
  you decided on never left the process.
- **The type comes from the stored record, never from the filename**, and it
  goes out with `nosniff`. Whatever decided the type at write time only means
  something if the read repeats it exactly.
- **`Content-Disposition` is an allow list, not a deny list.** Name the types
  that may render in place; everything else downloads. `application/pdf` is not
  on ours, because a same-origin PDF runs JavaScript in every major viewer.
- **Build the header with `mime.FormatMediaType`.** It quotes when it must,
  escapes embedded quotes, and switches to RFC 2231 `filename*=` for non-ASCII
  — three specs, none of which you will get right by hand. Its one wrong answer
  is `""`, which becomes `filename=""`; omit the parameter instead.
- **`gosec` G705 will flag the write**, because the bytes came from a stranger.
  That is the route's purpose, so the answer is a `//nolint` that names the
  mitigation, not a smaller feature. See the `fix-lint` skill.

Tests assert on `rec.Result().Header`, never `rec.Header()`. See the last
section of this file for why.

## Step 2b — every list route returns a page

Never a bare JSON array. `api.Page[T]` is the shape:

```json
{"items": [...], "next_cursor": "eyJjIjoi..."}
```

```go
limit, ok := api.Limit(w, r)          // ?limit=, default 50, clamped at 100
if !ok {
	return
}

params, ok := h.thingPage(w, r, limit) // reads ?cursor=, sets PageSize to limit+1
if !ok {
	return
}

rows, err := h.store.ListThings(r.Context(), params)
// ...
page, err := api.MapPage(rows, limit,
	func(row db.Thing) any { return thingCursor{Name: row.Name, ID: row.ID} },
	func(row db.Thing) thing { return thing{ID: row.ID, Name: row.Name} },
)
```

- **The envelope goes in before the first client, not when the table grows.**
  An array cannot become an object later without breaking every caller. The
  cursor is additive; the envelope is not.
- **The absence of `next_cursor` is the only end-of-collection signal.** Do not
  add a `has_more` boolean beside it — two sources of truth that can disagree.
  Kubernetes, Slack and AIP-158 all signal this way.
- **Query `limit + 1` rows.** The extra row answers "is there more" without a
  `COUNT` over the table, and `MapPage` drops it.
- **The cursor is built from the database row, not the wire type**, so it can
  use a column the response does not publish.
- **`ORDER BY` needs a unique tiebreak**, always: `ORDER BY name, id`. Ties on
  the sort column make the order unstable, and an unstable order makes a cursor
  skip or repeat rows.
- **Compare row constructors, not ANDed inequalities:**

  ```sql
  -- right: keyset semantics, and it can use the composite index
  WHERE (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid)

  -- wrong: silently drops every row that ties on created_at
  WHERE created_at < $1 AND id < $2
  ```

  The direction must match the sort: `<` with `DESC`, `>` with ascending.
- **Gate the cursor with a `bool`, not a nullable parameter:**
  `WHERE NOT sqlc.arg('use_cursor')::bool OR (...)`. Non-null parameters
  generate predictable Go types; `sqlc.narg` on a uuid does not.
- **A mangled cursor is a 400**, never a silent restart from the top — that
  turns a client's paging loop into an infinite one.

A listing whose size is bounded by something other than the table — a user's
own memberships, say — can answer `api.Page[T]{Items: items}` with no cursor.
Say why in a comment; the next reader will assume it was forgotten.

## Step 3 — map it in rbac.json

The handler has no authorisation code in it. Add a row instead:

```json
{ "method": "POST", "path": "/teams/{id}/agents",
  "scope": "team", "permission": "agent:write" }
```

`path` is the mux pattern with the prefix, exactly as `Router.Patterns()`
reports it. A route with no row **panics at startup** naming the route, and a
row for a route nothing serves fails `serve` — so this step cannot be
forgotten, only got wrong.

Choosing the scope, adding a permission, and what the loader touches are the
`authorise-a-route` skill. The short version:

| Path has `{id}`? | Needs a capability? | Scope |
| --- | --- | --- |
| yes | yes | `team` |
| yes | no | `member` (denies with 404) |
| no | yes | `any_team` |
| no | no | `authenticated` |

`any_team` on a route with `{id}` in it is the mistake to watch for — an admin
of one team passes it, so they reach every team.

Then pin it in `TestTheRouteMappingIsWhatWeIntend`
(`internal/store/rbac_test.go`), which fails when a route is added without a
decision recorded here.

In the handler, read what the guard produced:

```go
u := api.Caller(r)                       // the guard answered 500 if absent
id, ok := api.TeamFrom(r.Context())      // member and team scopes only
```

- **Never re-check what the scope checked.** A second `Can` is a query for an
  answer already obtained, and the two can disagree after a refactor.
- **Never `if role == "admin"`.** That is the line that makes swapping the
  authorisation backend an audit of every file.
- **Never parse `{id}` yourself** on a `member` or `team` route. The guard
  parsed it to run the check; parsing again is a chance to get a different
  answer than the one that was authorised.

## Step 3b — a write that references another resource

A body naming a second resource can take its id, its name, or either. The
question is not ergonomics, it is **what permission the caller needs to produce
the id.**

`POST /teams/{id}/members` takes `user_id` **or** `subject`. It accepts the
subject because the alternative was making every caller read `GET /users`
first, and that endpoint needs `user:read` somewhere — a far larger authority
than "add the person I can already name". Resolving it server-side leaves
`membership:write` in the named team as the only thing checked, which is the
authority actually being exercised.

Four rules when you do this:

- **Exactly one, and neither is a 400.** Preferring the id when both are given
  silently ignores a name somebody meant, and the two can name different
  people.
- **Resolve after the authorisation check, never before.** Otherwise the route
  answers "does this person exist" to anyone who can reach it — an existence
  oracle wearing a write endpoint. The two 403s must be byte-identical.
- **A key that is not unique answers 409, listing what matched.** `subject` is
  unique only per issuer. Picking a row puts the wrong person in a team and
  looks identical afterwards; the ids make a retry with the unambiguous form
  possible.
- **Nothing matching is 404, not 400.** The body is well-formed; the caller
  named somebody who does not exist.

The path is different: it stays an id. `/teams/{id}` accepting a name means
guessing whether a segment is a name or a uuid, and a team named like a uuid
breaks it. Clients resolve path references themselves — cheaply, when the list
they resolve against needs no special permission.

## Step 4 — which status

| Situation | Status | Why |
| --- | --- | --- |
| no token, bad token | 401 | the middleware answers; a handler never sees it |
| valid caller, not permitted | 403 | their token is fine, so do not tell them to log in again |
| malformed body, bad path value | 400 | say which field, never echo the value back |
| the resource does not exist | 404 | see the note below |
| a unique constraint rejected the write | 409 | the client cannot fix a 500 by changing the body |
| a foreign key rejected the write | 400, or 404 for the resource in the path | the constraint name says which |
| principal or user missing from the context | 500 | unreachable behind the middleware; if it fires the route is on the wrong mux |
| database or permission read failed | 500 | log the reason, tell the client nothing |

**404 versus 403 for a resource in another team.** Prefer 404: a 403 confirms
the id exists, which is a membership oracle. Use 403 only where the client
already knows the resource exists.

## Step 5 — wire it

```go
// cmd/brain/routers.go
func newRouters(cfg *config.Config, logger *slog.Logger, dbStore *store.Store, a *authz.Authorizer) []server.Router {
	return []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, a),
		users.New(logger, dbStore),
	}
}
```

`cmd/brain` is the only place that may know every dependency. Do not build an
`All()` helper inside `internal/api` — it would have to import every domain and
accept every dependency, which is the shared-handler problem arriving through
the back door.

Never register routes from `init()`. Dependencies cannot be passed, the set
becomes import-order dependent, and a blank import silently changes what the
server serves.

A router that must be reachable **without a token** — `docs`, `authconfig` —
uses `api.NewPublicRouter`. Its routes skip the guard entirely and must **not**
appear in `rbac.json`: they would be reported as mapped-but-unserved and stop
the boot.

## Step 5b — describe it in the spec

`api/openapi.yaml` is the contract the CLI is generated from, and a test
compares it against the routes the service registers. An endpoint added without
a description **fails the build**, in both directions — a path with no route
fails too.

Follow the `update-api-spec` skill. The short version: an `operationId`, every
status the handler can return, and `additionalProperties: false` on request
bodies because `DecodeJSON` rejects unknown fields.

## Step 6 — the tests every route owes

In the domain package, driving the mux rather than the handler function, so
the pattern is under test too:

```text
1  the happy path            status, and every field of the body
2  unauthorised              no token → 401, and the handler did not run
3  forbidden                 a caller the route's scope refuses, driving the
                             real guard — not an open one
4  the wrong method          POST on a GET route does not answer
5  only contracted fields    unmarshal to map[string]any and reject extras
```

Test 3 builds the guard from the scopes `rbac.json` gives this package's
routes, so the composition under test is the one production uses:

```go
New(logger, s, a).Register(mux, api.NewGuard(teamRoutes(t), a, logger))
```

An open guard is right only where the package has nothing to say about
authorisation. Where the tests assert 403s and 404s, drive the real thing.

Test 5 is the one people skip and the one that catches a leak: it fails when a
struct grows a field, rather than when a client notices.

For a route with a body, add: a malformed body, a missing required field, and a
field of the wrong type — all 400, and none of them reaching the store.

For a route that resolves a reference (step 3b), four more:

```text
6  exactly one form          neither and both are 400, and nothing was stored
7  nothing matched           404, not 400 — the body was fine
8  more than one matched     409 naming what matched, and nothing was stored
9  a forbidden caller cannot probe
                             the same 403, byte for byte, for a reference that
                             exists and one that does not — and the store was
                             never read
```

Test 9 is the one with no natural home. Every other test here is about what a
permitted caller gets; this is about what an unpermitted one can *learn*, and
without it a write endpoint quietly becomes an existence oracle for anybody who
can reach it. Assert on the body as well as the status — a reply that differs
by one word is still an oracle.

## Step 7 — prove it can fail

Break each guard and confirm the matching test fails, then restore:

| Break | Should fail |
| --- | --- |
| change the route's scope to `authenticated` in the test's route table | the forbidden test |
| change a `member` route to `team` | the 404-not-403 test |
| return 403 instead of 500 on a read error | the read-failure test |
| register the route on the public mux | the unauthorised test |
| ignore the second return of a `visible`-style check | the forbidden test |
| write the body before setting any header | the header tests |
| add a field to the response struct | the contracted-fields test |
| change `r.Get` to `r.Post` | the wrong-method test |
| resolve the reference before reading `api.Caller` | the probe test |
| prefer the id when both forms are given | the exactly-one test |
| return the first row instead of 409 | the ambiguity test |

The `visible` row is the one that will surprise you. A handler that ignores a
refusal still answers the refusal's status, because the check already wrote it
and `WriteHeader` only fires once — so it goes on to query the database and
serialise a body nobody can change the status of, and the test asserting 404
passes. **A status code cannot detect work done after a refusal.** Assert that
the collaborators were never called:

```go
	if len(f.store.attachmentListArgs) != 0 {
		t.Errorf("the list ran for a stranger: %v", f.store.attachmentListArgs)
	}
```

The rule this gives you: "the refusal changed nothing" has to be checked as
*did nothing*, on the fakes, because the visible outcome is already committed
before the damage happens.

**A mutation that does not compile proves nothing.** Deleting a branch usually
leaves a variable unused, and a build failure reads as "caught" if you are only
watching for a red test. Gate on `go build` first, and shape the mutation to
keep the identifier referenced — `if false && !allowed` rather than deleting
the block.

The public-mux row is worth doing once per domain. A route mounted outside the
authenticated mux passes every test written about its body.

Deleting the route's row from `rbac.json` is not a useful mutation: the process
will not start, which is the design. Confirm it once, then move on.

## Things that have gone wrong here

- **`Router` prefix joining.** `NewRouter("")` once rewrote the prefix to `"/"`,
  so `"/" + "/healthz"` became `"//healthz"` and `Register` panicked at startup.
  The join happens in `Register`, and the empty case is handled there.
- **A duplicate pattern panics**, because `ServeMux` does. That is deliberate: a
  routing conflict should fail the boot, not leave one route shadowed.
- **`httptest.ResponseRecorder.Header()` is the live map**, so a header set too
  late to reach the wire still appears there. Assert on `rec.Result().Header`.
  Measured, on a handler that writes the body before setting its headers:

  ```text
  rec.Header():        "nosniff"
  rec.Result().Header: ""
  ```

  This has now bitten twice, and the second time was on the route where the
  headers *are* the security control. Five assertions used the lenient form and
  every one of them passed against a handler whose headers never left the
  process.
- **A domain package that authorised itself.** `users` and `teams` each carried
  their own `Can` call and their own `Authorizer` interface, which meant every
  new route had to remember to add one. Both are gone; the guard does it, and
  `Register` will not mount a route without it.
- **`Resource.Kind` was declared and never used.** A discriminator nothing sets
  makes the first `switch` on it treat every existing call site as the empty
  case. Do not add a field before it has a reader.
