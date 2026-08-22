---
name: authorise-a-route
description: Decide and change who may call what — adding a permission, choosing a route's scope, adding a role, adding a scope, or working out why a request came back 403 or 404. Covers the request chain, the five scopes, rbac.json and what its loader touches, what is live versus what needs a restart, and the checks that refuse to boot. Read it before adding a route and before touching internal/authz.
---

# Authorise a route

Permissions are **data**; scopes are **code**. That asymmetry is the whole
design, and almost every question below is answered by working out which side
of it you are on.

- Adding a permission, creating a role, changing which permission a route
  requires — **rows.** No Go changes, and for roles no restart either.
- Adding a *scope* — **code.** Each scope is a different check, so it is a
  constant in `internal/authz/scope.go` and a case in the guard's switch.

## The request chain

```text
request
  → LogRequests
  → Authenticate      token → auth.Principal, or 401.        internal/middleware
  → ResolveUser       principal → *auth.User with .Teams.    internal/middleware
  → Guard             the route's scope decides the check.   internal/api
  → handler
```

Authentication is a separate question and a thin one: `internal/auth` turns a
bearer token into a `Principal` with no database access at all — either by
verifying an OIDC token against the provider's JWKS, or by comparing the
development token, which is only ever accepted on loopback. `ResolveUser` then
upserts the row and loads memberships. Everything after that is authorisation.

By the time the guard runs, `u.Teams` is already in memory. That is why two of
the five scopes cost no query.

## The five scopes

| Scope | The check | Permission | Denies with |
| --- | --- | --- | --- |
| `authenticated` | signed in, nothing more | never | — |
| `member` | belongs to the team in the path | never | **404** |
| `team` | holds the permission **in that team** | required | 403 |
| `any_team` | holds it **in any team at all** | required | 403 |
| `super_admin` | listed in `OPENARITY_SUPER_ADMINS` | never | 403 |

A super admin passes every one of them.

### Choosing one

```text
Is there {id} in the path?
├── yes ── does it need a specific capability?
│          ├── yes → team          POST /teams/{id}/members
│          └── no  → member        GET  /teams/{id}
└── no ─── does it need a specific capability?
           ├── yes → any_team      GET  /users
           └── no  → authenticated GET  /whoami

Creating the thing teams themselves live in → super_admin
```

- **`any_team` on a route with a team in its path is the mistake to watch
  for.** It is strictly weaker than `team`: an admin of *one* team passes it,
  so they would reach *every* team. `TestNoRouteWithATeamInItsPathIsAnyTeam` in
  `internal/store` refuses it, but know why rather than relying on the test.
- **`member` is not `team` with a permission everybody holds.** Belonging is a
  fact; a grant is configuration. Collapsing them makes "a member who cannot
  open their own team" reachable by leaving one line out of a role in a
  dashboard, and nobody would ever want that state.
- **`member` denies with 404 on purpose.** A 403 confirms the id is real, which
  lets an outsider walk uuids and learn which teams exist. `team` may answer
  403 because you had to be a member to get that far.
- **`authenticated` does not mean "everyone sees everything".** The handler
  filters. `GET /teams` is `authenticated`, and it returns your own teams
  unless you are a super admin.

## Adding a permission

Two lines in `internal/store/rbac.json`, and nothing else:

```json
  "permissions": [
    { "name": "agent:write", "description": "Create and change agents" }
  ],
  "roles": [
    { "name": "admin", "permissions": ["agent:write", "..."] }
  ]
```

- **The description is what a dashboard shows** instead of `agent:write`. A
  test fails without one.
- **Grant it to somebody.** A permission a route requires and no role holds is
  a route nobody can reach, and nothing logs — every request is simply a 403.
  `TestEveryPermissionARouteRequiresIsHeldBySomeRole` catches it.
- **`admin` holds every permission the file defines.** Its description says
  "full access within a team"; a test holds that true.
- **The name is `noun:verb`**, lower case. `agent:write`, not `write_agent` or
  `AgentWrite` — the whole set has to sort and read as a list in a UI.

## Pointing a route at it

```json
  "routes": [
    { "method": "POST", "path": "/teams/{id}/agents",
      "scope": "team", "permission": "agent:write" }
  ]
```

The `path` is the **mux pattern**, prefix included, exactly as `Router.Patterns()`
reports it — `/teams/{id}/agents`, not `/agents`. A key that does not match is
a route that will not start.

Then add it to `TestTheRouteMappingIsWhatWeIntend` in
`internal/store/rbac_test.go`. That test pins all eight mappings and fails when
one is added without a decision, which is the point: a route's scope is the
kind of thing that changes quietly and matters a lot.

## What the loader touches

`brain migrate up` applies the file, in one transaction, after the migrations:

| | |
| --- | --- |
| **permissions** | upserted; **never deleted** — an enterprise may have granted one |
| **roles named in the file** | given exactly the grants listed; a grant added by hand is drift |
| **roles not in the file** | untouched, so an operator's own role survives every deploy |
| **routes** | replaced wholesale — routes come from code, so the file is authoritative |

Consequences worth knowing before you rely on them:

- **A dashboard may create roles and grant permissions freely.** Those rows are
  never rewritten.
- **A dashboard must not edit `route_permissions`.** Every deploy replaces them.
- **Removing a permission from `rbac.json` does not delete it.** The foreign key
  would refuse anyway while any role holds it. Revoke the grants first.

## What is live and what needs a restart

| Change | Effect |
| --- | --- |
| grant or revoke a permission on a role | **immediate** — the guard reads `role_permissions` per request |
| create a role | immediate |
| add a permission | immediate |
| a route's scope or permission | **restart** — the route table is read once at boot |

The route table is deliberately not read per request: it changes on deploy, so
a lookup would be a database round trip on the authorisation path for an answer
that cannot have changed. If that ever needs to be live, the guard already
holds a `authz.Routes` map — something just has to swap it (SIGHUP, a poll, or
`LISTEN`/`NOTIFY`), and no other code changes.

## Adding a scope

Only when a genuinely different *question* is being asked. Four places:

1. `internal/authz/scope.go` — the constant, and either
   `scopesWithAPermission` or `scopesWithoutAPermission`.
2. `internal/api/authorize.go` — a `case` in `Guard.check`.
3. The migration's `CHECK (scope IN (...))` and the permission/scope agreement
   `CHECK`. **A new migration**, never an edit to an applied one.
4. `internal/api/authorize_test.go` — `TestNoCallerIsAnInternalError` sweeps
   every scope; add yours.

**Do not add a `default:` arm that falls through to the handler.** The existing
one answers 500 and logs, and it is unreachable because `Routes.Add` refuses a
scope with no case. `exhaustive` reports the missing case only because
`.golangci.yml` sets `default-signifies-exhaustive: false` — with the default,
a missing scope reports nothing.

## What a handler may and may not do

The guard decides **whether the handler runs**. The handler decides **what it
returns**. Both use the same facts; only the second is the endpoint's job.

```go
// Right: GET /teams is `authenticated`, and this is the content decision.
if !h.authz.IsSuperAdmin(u) {
	return h.myTeams(r.Context(), u)
}
```

- **Never re-check what the scope already checked.** A second `Can` in the
  handler is a query for an answer already obtained, and the two can disagree
  after a refactor.
- **Never `if role == "admin"`.** That is the line that turns swapping the
  authorisation backend into an audit of every file.
- **Read the caller with `api.Caller(r)`** and the team with
  `api.TeamFrom(r.Context())`. The guard parsed the uuid to run its check;
  parsing it again is a chance to get a different answer.
- **A domain package should not import `internal/authz` at all.** `users` does
  not. `teams` does, for one `IsSuperAdmin` used to filter a list, and declares
  it as `SuperAdmins` rather than `Authorizer` — the name says it answers a
  question about people, not that it decides access.

## What fails the boot, and what it says

Three checks, all before the first request:

| Situation | Where | Message |
| --- | --- | --- |
| a protected route with no row | `Router.Register` panics | `route "GET /x" is not in rbac.json` |
| a row for a route nothing serves | `serve` returns an error | `rbac.json maps routes this server does not serve: …` |
| an empty `route_permissions` | `newGuard` returns an error | `no route permissions in the database — run brain migrate up` |

Panicking rather than returning an error in `Register` is the house pattern:
`NewRouter` panics on a malformed prefix and `ServeMux` panics on a duplicate
pattern, for the same reason — a wiring mistake must fail the boot rather than
serve.

A **public** router is exempt. `NewPublicRouter` mounts outside authentication,
so there is no caller to authorise, and its routes must **not** appear in
`rbac.json` — they would be reported as unused and stop the boot.

Everything on the **webhook listener** is in that category. A provider webhook
has no caller at all: it proves a signature over the raw body against one
channel's secret, so there is no user, no role and nothing to look up. Adding a
`/hooks/...` row to `rbac.json` fails `serve` with *maps routes this server
does not serve*, because the guard never sees those routes.

## Reading a denial

| You got | It means |
| --- | --- |
| 401 | no token or a bad one. The guard never ran. |
| 403 on a `team` route | you are in the team, your role lacks the permission |
| 403 on an `any_team` route | no role you hold anywhere has the permission |
| 403 on a `super_admin` route | you are not in `OPENARITY_SUPER_ADMINS` |
| 404 on a `/teams/{id}` route | you are not a member — or it does not exist. Deliberately the same. |
| 400 `id must be a uuid` | the guard refused before asking anything |
| 500 | the permission read failed, or there was no caller on the context |

That last one matters: **a failed permission read is *unknown*, not *denied*.**
Answering 403 makes a database blip look like a permissions problem and sends
whoever is on call to audit roles that were never wrong.

No denial names what was missing. A caller learning which permission a route
wants is being handed a map of the model.

## Tests a change here owes

| Change | Where the test goes |
| --- | --- |
| a new permission | `internal/store/rbac_test.go` — it is granted to some role |
| a new route mapping | `TestTheRouteMappingIsWhatWeIntend` |
| a new scope | `internal/api/authorize_test.go`, every sweep in it |
| a domain route | its own package, with the real scopes — see below |

Domain tests build the guard from the scopes `rbac.json` actually gives their
routes, not an open one:

```go
func teamRoutes(t *testing.T) authz.Routes {
	rs := authz.NewRoutes()
	membership := "membership:write"
	// ... one Add per route, matching rbac.json
	return rs
}

New(logger, s, a).Register(mux, api.NewGuard(teamRoutes(t), a, logger))
```

An open guard is right only where the package has nothing to say about
authorisation — `users`, `whoami`, `docs`. Where the tests assert 403s and
404s, drive the real thing.

## Things that have gone wrong here

- **A `switch` missing a scope.** `ScopeTeam` was left out of `Guard.check`
  once, so both membership-writing routes answered 500. `exhaustive` catches
  it; the `default:` arm made it a clean error rather than a silent allow.
- **`server.New` not copying `deps.Guard`.** The field existed, the type was
  right, everything compiled, and `Register` dereferenced nil at boot. Only a
  test driving the wired object found it.
- **`ON DELETE RESTRICT` raises 23001, not 23503.** A test asserting
  `foreign_key_violation` on a refused delete is really asserting that somebody
  wrote `NO ACTION`.
- **`role_permissions.action` had no foreign key** for its whole first life, so
  `membership:writ` inserted happily, granted nothing, and said nothing. That
  hole is what `permissions` exists to close.
