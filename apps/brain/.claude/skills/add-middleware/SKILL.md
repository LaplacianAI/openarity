---
name: add-middleware
description: Add an HTTP middleware to the brain — request logging, authentication, request IDs, panic recovery, rate limiting, body size limits, or anything else that wraps a handler. Covers the signature, wiring into both listeners, chain order, and the tests, so it cannot end up built but never applied.
---

# Add a middleware to `internal/middleware`

Everything that wraps an `http.Handler` lives in one package.

**One exception, and it is deliberate: the authorisation guard.** It lives in
`internal/api` and is applied by `Router.Register`, not by `server.New`,
because the check a route needs depends on *which route it is* and the `Router`
is the only thing that knows a handler's method and pattern. Wrapping there
also removes the failure step 4 below warns about — `Register` will not compile
without a guard, so it cannot be built and never applied. See the
`authorise-a-route` skill. Nothing else gets this treatment. A middleware is
not done until it is wired into `server.New` and a test drives the wired
object — the failure mode here is silent, and no linter catches it.

## Step 1 — the file

One middleware per file, named after what it does:

```text
internal/middleware/middleware.go   the Middleware type, shared helpers
internal/middleware/logging.go      LogRequests
internal/middleware/auth.go         RequireSession
```

Anything used by exactly one middleware — a response wrapper, a header
constant — stays in that middleware's file. It moves to `middleware.go` only
when a second file needs it.

## Step 2 — the signature

Dependencies in the outer call, the handler in the inner one:

```go
func RequireSession(store *store.Sessions) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ... work before
			next.ServeHTTP(w, r)
			// ... work after
		})
	}
}
```

Never `func RequireSession(store *store.Sessions, next http.Handler)`. The
two-argument form nests inside-out and becomes unreadable at three:

```go
LogRequests(logger, RequestID(RecoverPanic(logger, RequireSession(store, mux))))
```

The closure form lets each middleware take its own dependencies and still chain
in reading order.

Name it for what it does to the request — `RequireSession`, `LogRequests`,
`LimitBody`. Not `AuthMiddleware`: the package name is already `middleware`, so
`middleware.AuthMiddleware` stutters.

## Step 3 — wrapping the response

Only if the middleware needs to know what the handler answered.
`http.ResponseWriter` is write-only — there is no way to read the status back —
so wrap it:

```go
type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
```

Embedding promotes `Header()` and `Write()` unchanged; only `WriteHeader` is
overridden.

**Initialise `status` to `http.StatusOK`.** A handler that only calls `Write`
never calls `WriteHeader` — Go sends 200 implicitly. Start at 0 and every
successful request records status 0.

`internal/server` already has one `recorder` in `logging.go`. If a second
middleware needs the same thing, move it to `middleware.go` rather than
declaring another.

## Step 4 — wire it into `server.New`

**This is the step that gets skipped, and nothing warns you.** `unused` assumes
an exported identifier has a caller in another package, so a middleware that is
built and never applied passes `make check` completely.

```go
func New(cfg *config.Config, logger *slog.Logger) *Server {
	logRequests := middleware.LogRequests(logger)

	return &Server{
		api:     newHTTPServer(cfg.APIBind, logRequests(apiHandler())),
		webhook: newHTTPServer(cfg.WebhookBind, logRequests(webhookHandler())),
		logger:  logger,
	}
}
```

Decide deliberately whether it applies to **both** listeners. Session auth is
API-only — the webhook listener authenticates by request signature, not by
credentials, and wrapping it in session auth would reject every provider.

Once there is a second middleware, add `Chain` and list them in order rather
than nesting.

## Step 5 — order

Order is behaviour, not style. Outermost first:

| Position | Middleware | Why there |
|---|---|---|
| outermost | `RecoverPanic` | must catch panics from everything inside it, including other middleware |
| | `RequestID` | later middleware and handlers log the id, so it has to exist first |
| | `LogRequests` | inside RequestID so the id is on the line; outside auth so rejected requests are still logged |
| | `LimitBody` | before anything reads the body |
| innermost | `RequireSession` | last, so a 401 is still logged and still has an id |

The rule: anything that must observe a request it might reject goes **outside**
the thing that rejects it.

## Step 6 — the traps

- **Never log `r.URL.String()`.** Credentials travel in query strings and the
  log shipper takes them off the box. Log `r.URL.Path`.
- **Never read the body before signature verification.** Webhook adapters
  verify against the exact bytes. A middleware that decodes JSON first breaks
  every provider.
- **Skip `/healthz`** in anything that logs. Kubernetes probes it every ten
  seconds on two listeners — roughly 17k lines a day. Let the response through
  untouched; only the log is skipped.
- **Do not swallow panics** unless the middleware is `RecoverPanic`. Recovering
  and continuing leaves a half-written response.

## Step 7 — tests

`internal/middleware/<name>_test.go`, mirroring the source file. Every
middleware needs at least these four:

1. **It does its job** — the fields are logged, the 401 is returned, the header
   is set.
2. **It does not alter the response** — status, body and headers pass through
   a handler that sets all three. A response wrapper that swallows the body is
   easy to write and invisible until production.
3. **It handles the implicit 200** — a handler that only calls `Write`.
4. **It passes panics through**, unless it is `RecoverPanic`.

Then the wiring test in `internal/server/server_test.go`. Extend
`TestNewWrapsBothListenersInTheRequestLogger` or add its equivalent — build a
`Server` with a recording logger, send a request through each listener's
handler, and assert the middleware ran. **This is the only thing that catches
step 4 being skipped.**

Handler identity cannot be asserted: once middleware is applied the handler is
an `http.HandlerFunc`, and `==` on a func type panics at runtime. Assert
behaviour.

## Step 8 — verify

```sh
cd apps/brain && make check && make cover
```

Coverage is at 100%. A middleware with an untested branch will drop it.
