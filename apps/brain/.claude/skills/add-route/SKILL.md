---
name: add-route
description: Add an HTTP route to the brain's API — a new endpoint on an existing domain, or a whole new domain package. Covers where the route lives, the Router, per-package dependencies, the response contract, which status code each failure gets, where the authorisation check goes, and the tests every route owes. Use for every endpoint.
---

# Add a route to the API

Every API route lives in a domain package under `internal/api/`, is mounted by
`internal/server`, and runs behind authentication and user resolution. A route
is not done until it has an authorisation check, a response contract, and the
five tests at the bottom.

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

// Authorizer is the authorisation question this package asks — nothing more.
type Authorizer interface {
	IsSuperAdmin(u *auth.User) bool
}

// handler is unexported and holds exactly what this package needs. A missing
// dependency is then a compile error rather than a nil at request time.
type handler struct {
	logger *slog.Logger
	store  Store
	authz  Authorizer
}

func New(logger *slog.Logger, s Store, a Authorizer) *api.Router {
	h := &handler{logger: logger, store: s, authz: a}

	r := api.NewRouter("/teams")
	r.Post("", h.create)
	r.Get("", h.list)
	r.Get("/{id}", h.get)
	return r
}
```

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

## Step 3 — the authorisation check

Every route that acts on a team calls `Can` before doing anything:

```go
allowed, err := h.authz.Can(r.Context(), user, authz.ActionAgentWrite,
	authz.Resource{TeamID: teamID})
if err != nil {
	h.logger.Error("authorisation check failed", "subject", user.Subject, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
	return
}
if !allowed {
	http.Error(w, "forbidden", http.StatusForbidden)
	return
}
```

- **`Can` is the only place a role is interpreted.** Never `if role == "admin"`
  in a handler — that is the line that makes swapping the authorisation backend
  an audit of every file.
- **The error and the denial are different.** A failed permission read is
  *unknown*, not *denied*: 500, not 403. Collapsing them makes a database blip
  look like a permissions problem.
- **Actions are constants in `internal/authz`.** A new one goes in `action.go`,
  in `AllActions`, and in a seed migration — a test fails if any of the three is
  missed.

## Step 4 — which status

| Situation | Status | Why |
| --- | --- | --- |
| no token, bad token | 401 | the middleware answers; a handler never sees it |
| valid caller, not permitted | 403 | their token is fine, so do not tell them to log in again |
| malformed body, bad path value | 400 | say which field, never echo the value back |
| the resource does not exist | 404 | see the note below |
| principal or user missing from the context | 500 | unreachable behind the middleware; if it fires the route is on the wrong mux |
| database or permission read failed | 500 | log the reason, tell the client nothing |

**404 versus 403 for a resource in another team.** Prefer 404: a 403 confirms
the id exists, which is a membership oracle. Use 403 only where the client
already knows the resource exists.

## Step 5 — wire it

```go
// cmd/brain/routers.go
func newRouters(logger *slog.Logger, dbStore *store.Store, a *authz.Authorizer) []server.Router {
	return []server.Router{
		whoami.New(logger),
		teams.New(logger, dbStore, a),
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

## Step 6 — the tests every route owes

In the domain package, driving the mux rather than the handler function, so
the pattern is under test too:

```text
1  the happy path            status, and every field of the body
2  unauthorised              no token → 401, and the handler did not run
3  forbidden                 a caller without the action → 403
4  the wrong method          POST on a GET route does not answer
5  only contracted fields    unmarshal to map[string]any and reject extras
```

Test 5 is the one people skip and the one that catches a leak: it fails when a
struct grows a field, rather than when a client notices.

For a route with a body, add: a malformed body, a missing required field, and a
field of the wrong type — all 400, and none of them reaching the store.

## Step 7 — prove it can fail

Break each guard and confirm the matching test fails, then restore:

| Break | Should fail |
| --- | --- |
| delete the `Can` check | the forbidden test |
| return 403 instead of 500 on a read error | the read-failure test |
| register the route on the public mux | the unauthorised test |
| add a field to the response struct | the contracted-fields test |
| change `r.Get` to `r.Post` | the wrong-method test |

The third row is worth doing once per domain. A route mounted outside the
authenticated mux passes every test written about its body.

## Things that have gone wrong here

- **`Router` prefix joining.** `NewRouter("")` once rewrote the prefix to `"/"`,
  so `"/" + "/healthz"` became `"//healthz"` and `Register` panicked at startup.
  The join happens in `Register`, and the empty case is handled there.
- **A duplicate pattern panics**, because `ServeMux` does. That is deliberate: a
  routing conflict should fail the boot, not leave one route shadowed.
- **`httptest.ResponseRecorder.Header()` is the live map**, so a header set too
  late to reach the wire still appears there. Assert on `rec.Result().Header`.
- **`Resource.Kind` was declared and never used.** A discriminator nothing sets
  makes the first `switch` on it treat every existing call site as the empty
  case. Do not add a field before it has a reader.
