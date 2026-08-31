# brain

The Openarity backend. One Go module, one binary, five packages drawn exactly
as services. Design lives in the
[openarity-ideation](https://github.com/LaplacianAI/openarity-ideation) repo,
under `Tech Design/HLD-V1/HLD.md`.

## Working agreement

**The user writes the production code. Claude writes the tests and tries to
break it, explains, and reviews.**

- Do not write implementation code, scaffold files, or finish a function unless
  explicitly asked. Guide step by step, method by method.
- When code is pasted: review it, list what is missing or wrong, then write
  tests that attack it — not tests that confirm it works.
- Explanations stay short and plain, and always include a small snippet.
  Functions in snippets stay small: one job, few lines, no cleverness.
- Verify library behaviour with a throwaway probe under the scratchpad rather
  than asserting it from memory. Both `net.SplitHostPort` and `url.Parse`
  turned out far more lenient than they look.

## Skills

- **`add-env-var`** — adding any new environment variable. Covers the struct
  field, the enum type if the value is a fixed set, validation, redaction in
  `String()`, and the three test files. Use it every time; a field wired into
  four of the five places is the normal failure.
- **`add-middleware`** — anything that wraps an `http.Handler`: auth, request
  IDs, panic recovery, rate limits, body size limits. Covers the signature,
  wrapping the response writer, chain order, and wiring into `server.New`. Use
  it every time; a middleware that is built and never applied passes every
  linter and every test except the wiring one.
- **`authorise-a-route`** — anything about who may call what: adding a
  permission, choosing a route's scope, adding a role, or working out why a
  request came back 403 or 404. Covers the five scopes, `rbac.json`, what is
  live versus what needs a restart, and the checks that fail the boot. Read it
  before adding a route, and before touching `internal/authz`.
- **`add-route`** — every API endpoint, and every new domain package under
  `internal/api`. Covers where the route lives, the `Router` and its prefix,
  per-package dependencies, the response contract, which status each failure
  gets, and the five tests a route owes. Use it
  every time; a route registered on the wrong mux passes every test written
  about its body.
- **`write-migration`** — every schema change. Covers the goose file format,
  `lock_timeout`, expand-contract for column changes, batched backfills, and
  when an index needs `CONCURRENTLY`. Use it every time: the migration that
  freezes a table in production is indistinguishable from a safe one when the
  table is empty.
- **`write-query`** — every query that reads or writes Postgres. Covers the
  sqlc annotations, regenerating and committing the output, type overrides,
  when a write needs `InTx`, and the batch and copy modes. Use it every time;
  never hand-write the Go that runs SQL.
- **`write-tests`** — every test, any package. Covers naming, when
  `t.Parallel()` is allowed, contexts and ports and timing, what a fake owes
  you, and step 7: break the thing and confirm the test fails. Read it before
  writing a concurrency test.
- **`fix-lint`** — any golangci-lint or gofumpt failure, and any change to
  `.golangci.yml`. Lists the linters that actually fire here and the correct
  fix for each. The fix is almost never a `nolint`.
- **`test-with-postgres`** — any test that needs a real database. Covers
  skipping when none is available, one schema per test, why these cannot be
  `t.Parallel()`, and how to check a test would actually fail. Read step 5
  before writing any concurrency test: we shipped one that passed with the
  guard removed.

## Layout

```text
apps/brain/
  cmd/brain/
    main.go            main(), run(), execute()
    command.go         argument parsing — pure, no dependencies
    serve.go           the serve role
    migrate.go         the migrate role
    reap.go            the reap role
    logger.go          newLogger()
  internal/config/     configuration: load, validate, redact
  internal/server/     the two listeners: build, run, shut down; mounts Routers
  internal/middleware/ request logging, authentication, user resolution
  internal/auth/       token in, Principal out — no database
  internal/authz/      Can, CanInAnyTeam, the five scopes, the route table
  internal/api/        Router, WriteJSON, DecodeJSON, Page
    authorize.go       the Guard: one check per route, chosen by its scope
    <domain>/          one package per domain, each its own Router
      <domain>.go      handler, New, the handlers
      schema.go        request and response structs — the wire contract
  internal/gateway/    a provider's webhook in, normalised messages out
    providertest/      the conformance suite every adapter runs
    <provider>/        one package per platform; custom is the reference
  internal/secrets/    the port: Store, Writer, Creator, Prober, secret paths
    datakeys.go        a team's data key, generated once and never replaced
    openbao/           the AppRole client — the only thing that reaches OpenBao
    static/            the in-process fallback, for a brain with no OpenBao
  internal/objects/    the port: Store, Writer, object keys and team prefixes
    encrypt.go         AES-256-GCM over any backend — a layer, not a backend
    sniff.go           what a file is, and whether it may be stored
    s3/                the S3 API — MinIO, Ceph, R2, GCS, AWS
    filesystem/        a volume, for a single-host deployment with no S3
    inmemory/          the in-process fallback; attachments die with the process
  internal/reaper/     the sweep: one loop, one Effect per outside system
  internal/store/      Postgres: pool, migrations, queries
    migrations/        goose .sql files, embedded into the binary
    rbac.json          the permissions, roles and route mappings we ship
  Makefile             build and code quality targets
  .golangci.yml        linters and formatters
```

One binary. If a worker or another role appears it becomes an argument
(`brain worker`), not a second `cmd/`. Same config, same dependencies, one
image, one version — two binaries means every dependency bump is two images
that can drift.

`cmd/brain` is the composition root and the only place that knows every
dependency. `internal/server` never learns what Postgres is; `internal/api`
will never learn what a port is.

Each app in the monorepo is a separate Go module. `apps/brain/internal/` is
unreachable from `apps/cli/` by construction — Go's `internal` rule is scoped
to the module root. Nothing is shared between apps; the only thing crossing an
app boundary is `api/openapi.yaml`, and it is a spec, not code.

If brain and another app appear to need the same Go code, that is the signal
they should be talking over HTTP instead.

## Conventions

- **Initialisms keep one case**: `URL`, `DSN`, `API`, `ID`, `DB`. Unexported
  names starting with one go all-lowercase: `redactURL`, but `urlParser`.
  `revive`'s `var-naming` enforces this.
- **File names match content**: singular for one type (`config.go`), plural for
  several related things (`enums.go`). Split by job, not by size — loading,
  validating and the enums are three files.
- **A type, its constants and its methods stay together** in one file.
- **Interfaces only where a second implementation exists.** Methods on structs
  are the right amount of OOP here; an interface with one implementation just
  costs a hop when reading. Accept interfaces, return structs.
- **Do not create a package until it has an occupant.** No `util` package —
  name packages after what they do (`redact`, not `util`). A little copying
  beats a little dependency.
- **Tests mirror source files**: `validate.go` has `validate_test.go`.
- **Tests inject configuration, never mutate process env.** `load(map[...])`
  keeps tests isolated and `t.Parallel()`-safe; `t.Setenv` does not.
- **`os.Exit` appears only in `main`, and `main` holds no defers.** `os.Exit`
  skips deferred functions entirely, so a `defer` beside it is dead code. That
  is the entire reason `run(ctx, out)` exists — everything that needs cleanup
  lives there and returns an error instead. `gocritic`'s `exitAfterDefer`
  catches regressions.
- **Take the context and the writer as parameters.** Anything that builds its
  own context cannot be cancelled by a test, and anything that writes to
  `os.Stdout` directly cannot be observed by one. Both turn a fast test into a
  ten-minute timeout.
- **Tests reserve a real ephemeral port; they never hardcode one and never use
  port 0.** Bind `127.0.0.1:0`, read `l.Addr()`, close it, use the address.
  A hardcoded port collides with a running brain, and `checkHostPort` rejects
  port 0 outright.
- **`noctx` bans the context-free constructors.** Use
  `httptest.NewRequestWithContext`, `(*net.ListenConfig).Listen` and
  `exec.CommandContext` — never `httptest.NewRequest`, `net.Listen`,
  `http.Get` or `exec.Command`. In tests the context is `t.Context()`.
- **Lifecycle tests poll, they do not sleep.** Wait until `/healthz` answers
  200 before asserting anything, with a deadline that fails the test. A
  `time.Sleep` long enough to be reliable is long enough to be slow, and short
  enough to be fast is flaky in CI.
- **Use the stdlib type when it already encodes the constraint.** `LogLevel` was
  a hand-written enum until `slog.Level` turned out to be a `TextUnmarshaler`
  that parses case-insensitively and rejects unknown names with a better
  message. Write your own type when the constraint is yours — `Environment` has
  no stdlib equivalent and stays. Probe the stdlib before writing the enum.
- **The logger is a parameter, never a global.** `slog.Default()` is a hidden
  dependency and cannot be swapped safely under `t.Parallel()`. It reaches
  `internal/server` through `New`, and a nil one is left to panic rather than
  defaulted — see the trust rule below.
- **Log fields, not sentences.** `logger.Info("request", "status", 202)`, never
  `logger.Info(fmt.Sprintf("request returned %d", 202))`. The point is querying
  by field. Never log a whole struct: `"config", cfg` with `%+v` bypasses the
  `String()` that was redacting the password.
- **Log `r.URL.Path`, never `r.URL.String()`.** Credentials travel in query
  strings, and the log shipper takes them off the box.
- **Middleware returns a `Middleware`, it does not take `next` directly.**
  `func LogRequests(logger) Middleware` — dependencies in the outer call, the
  handler in the inner one. The two-argument form nests inside-out and becomes
  unreadable at three middlewares.
- **An exported identifier that nothing calls is invisible.** `unused` assumes
  an exported function has a caller in another package, so a middleware that is
  built and never applied passes every linter. Only a test that drives the wired
  object catches it.
- **Validate what comes from outside; trust what comes from the composition
  root.** `config.Load` validates because the environment is outside the
  program. `server.New` does not check its logger for nil because `run` is
  inside it — a nil there is a programming error, and a panic with a stack
  trace beats a silent fallback.
- **Cheapest failure first.** `run` parses arguments before loading config, and
  loads config before dialling Postgres. A typo in a Kubernetes Job spec should
  fail instantly with `unknown command "migrat"`, not after a connect timeout
  with an error blaming the database.
- **Parsing is separate from execution.** `parse` turns `[]string` into a
  `command` and is pure — no config, no database, no environment. `execute`
  validates nothing because `parse` already did. That is what lets the argument
  tests run with nothing set up.
- **A fixed set of strings is a defined type, not a `string`.** `commandName`
  and `direction` exist so `exhaustive` fails the build when a new role is
  added and a switch forgets it. That guarantee depends on
  `default-signifies-exhaustive: false` — it was `true` here for three steps,
  and adding a `commandName` reported 0 issues. Keep the `default:` arm anyway:
  it is unreachable, it returns a clear error rather than silence, and it is
  tested.
- **Never put a DSN in an error message.** pgx already redacts the password in
  its own errors — `postgres://user:xxxxx@…` — and wrapping with the raw string
  undoes that. Errors reach stderr and the log shipper.
- **Never delete error handling to raise a coverage number.** When a branch is
  untestable, say so in a comment and pin the assumption with a test that fails
  if it stops holding — see `TestSessionLockerCannotFailWithoutOptions`.

## Commands

```sh
make                    # list targets
make check db=postgres  # everything CI runs — see the note below about db=
make run                # run the server; sources .env if present, Ctrl-C to stop
make cover              # coverage, fails below the threshold
make cover-html db=postgres      # the annotated HTML report
make testdb db=openarity_test    # create a test database — once per machine
make generate           # regenerate everything generated (today: sqlc)
make fmt                # apply gofumpt and fix import order
make tools              # reinstall tooling — rerun after a Go upgrade
```

`make check` is the real gate; run it before saying anything is done.

**Always pass `db=` when measuring coverage.** Database tests skip when
`BRAIN_TEST_POSTGRES_DSN` is empty, and `db=name` is what sets it — `host`,
`port`, `user` and `sslmode` default around it. Without it `serve`, `migrateUp`
and every query read 0% and the total drops from 97.6% to 77.8%, which looks
like a coverage problem and is not one. `make cover` warns when the variable is
unset for exactly this reason. Never read a coverage report that was produced
without a database.

**`db=postgres` leaves `port` at 5432, which is rarely the compose database.**
On a machine with Postgres installed as well as running in compose, `make check
db=postgres` reaches the installed one. `internal/store` now refuses a server
below 18 and names the address it reached, so this fails loudly instead of
producing one baffling assertion — but pass `port=` and it never comes up.

**After a Go toolchain upgrade, run `make tools`.** Anything installed with
`go install` is compiled against the Go present at the time, and both
`golangci-lint` and `gopls` break with a version-mismatch error until
reinstalled.

## Decisions worth not relitigating

- **Config is env-only.** No config files, no flags for the server, no Viper.
  Kubernetes injects env natively, and the config surface stays small because
  secrets live in Vault rather than here.
- **Every pluggable subsystem names its backend in env.** `SECRETS_BACKEND` is
  static, openbao or vault; `OBJECTS_BACKEND` is memory, filesystem or s3. Never
  inferred from which settings happen to be filled in — that works for two
  options where one is clearly "not configured", and breaks at three, where
  "endpoint set" and "path set" can both be true, both false, or disagree. The
  enum's `UnmarshalText` turns a typo into a boot failure listing the valid
  values, rather than a silent fall back to the store that holds nothing. The
  in-memory fallback of each is refused outside development.
- **A port package, one package per adapter.** `gateway` was the first to do
  it and `secrets` and `objects` now match: the interface and its shared
  helpers live in the parent, each implementation in its own subpackage
  exporting only `New()`. The adapter type stays unexported, so there is one
  way to construct it and no way for one adapter to reach into another's
  internals — the compiler enforces what review otherwise has to.
- **A constructor returns the narrowest useful interface.** `openbao.New` and
  `inmemory.New` return the read-only `Store` even though both values also
  implement `Writer`. `serve.go` has to assert for the writer and hand it to
  exactly one router, which makes taking write access a visible line rather
  than an ambient capability.
- **Postgres is truth, the graph is an index.** Nothing is written only to
  FalkorDB; every node has a Postgres row behind it and the graph is
  rebuildable at any time.
- **The test suite's Postgres floor is 18; the brain's is 13.** Both measured,
  not read off a changelog. 13 is where `gen_random_uuid()` became built-in, and
  every migration applies and rolls back on 13 through 18. 18 is the first
  release to raise `SQLSTATE 23001` for an `ON DELETE RESTRICT` refusal, which
  `schema_test.go` asserts; 13 through 17 report it as `23503`. `internal/store`
  has a `TestMain` that refuses below 18 rather than skipping — the bug it
  exists for was a suite running green against a server nobody chose, and a
  skip is that same silence in a different colour.
- **Two listeners, one process.** The API binds loopback; webhooks bind
  publicly. The auth models are opposites — user credentials versus request
  signature — so they never share a listener. Signature verification needs the
  raw request body, so nothing may parse it first. They never share a mux
  either: one handler on both ports exposes every API route publicly the day
  the routes diverge.
- **Listeners share a fate.** If one fails to bind, the other is shut down and
  the process exits non-zero. Half-alive is the worst state — the pod passes
  its probe while silently dropping webhooks, providers retry a few times and
  give up, and nothing is logged. Fail fast and let Kubernetes crash-loop it
  into an alert.
- **`http.ErrServerClosed` is success.** `ListenAndServe` always returns a
  non-nil error; a clean `Shutdown` returns that one. Treat it as a failure and
  every rolling update is recorded as a crash.
- **Server timeouts are constants, not configuration.** Go's zero-value
  `http.Server` has no timeouts at all, which leaves the public webhook port
  open to Slowloris — `gosec` G112 fails the build on a missing
  `ReadHeaderTimeout`. The same values are right in every environment, so they
  stay named constants. Promote one to config only when a real deployment needs
  a different value. Note that `WriteTimeout` bounds handler execution too: the
  streaming endpoints will need `http.NewResponseController(w).SetWriteDeadline`
  to opt out per route.
- **Secrets are references.** A row or config field holds a Vault path, never a
  value. Only `internal/secrets/openbao` reaches a secret backend.
- **Attachments are encrypted before they leave the process.** AES-256-GCM
  under a per-team key held at `teams/<team_id>/attachments`, so the object
  store holds `nonce || ciphertext || tag` and never a key, and reading one
  team's files needs OpenBao *and* that team's key. Encryption is a layer over
  whichever backend `OBJECTS_BACKEND` selected, not a fourth backend — it lives
  in `internal/objects/encrypt.go` rather than beside `s3`, because nothing
  sets `OBJECTS_BACKEND=encrypted`.
- **The team is a parameter, never parsed out of the object key.** The key is a
  string on a database row, so deriving the decryption key from it would let a
  tampered row choose which key is used. Taking the team from the authenticated
  route makes `objects.InTeam` a check rather than a derivation, and every
  refusal is asserted to have changed nothing.
- **The object's key is GCM's additional data.** Additional data is bound, not
  encrypted — it costs nothing and stops somebody with write access to the
  bucket copying one attachment's bytes onto another's key, which without it
  decrypts perfectly: same team, same key, valid tag, and a person served a
  file the row did not name. It forecloses renaming an object, which we do not
  do, and which would fail at the first read rather than silently.
- **A key is decided once, and everyone else agrees with the decision.** A
  team's key is generated on first use, so two concurrent first uploads race.
  `Put` cannot settle it — KV v2 overwrites, so both writers succeed and the
  loser's data is sealed under a key nothing holds. `secrets.Creator` is the
  check and the write as one operation: `cas=0` in OpenBao, one lock in the
  static store. The loser reads the winner's key rather than retrying, because
  retrying returns `ErrExists` forever.
- **An attachment's type comes from its bytes, and the list says what is
  permitted rather than what is not.** A provider's declared content type is
  attacker-influenced; a deny list is a list of the attacks already known.
  Both halves are needed and only one lives here: sniffing at write time is a
  claim the read path has to keep, by serving the recorded type with
  `X-Content-Type-Options: nosniff` and never a type derived from a filename.
  An SVG carrying a script sniffs as `text/plain`, and a PNG with markup
  appended still sniffs as `image/png` — both inert only while served as what
  they sniffed to.
- **An attachment's team comes through its session, never through its
  channel.** `sessions.team_id` is the authoritative copy and always present;
  `sessions.channel_id` is nullable, because the dashboard and the API start
  sessions with no channel behind them. A composite foreign key keeps the two
  team ids equal whenever there is a channel, so joining through sessions is
  never wrong and is sometimes the only join that answers. `GetAttachmentWithTeam`
  returns the team in the same round trip as the row, so authorisation never
  needs a second one.
- **`attachments` carries `session_id` as well as `message_id`, and the pair is
  a foreign key.** The copy is redundant and cannot drift: `messages` has a
  unique key on `(id, session_id)`, so an attachment naming a session its
  message is not in is refused by the database rather than by whoever
  remembered. Same mechanism as `sessions.team_id` against `channels.team_id`.

  It is there because "every attachment in this session" is what an agent asks
  to build context — a message can name a file that arrived twenty messages
  ago — and no index can express that through the join. Measured on 200
  sessions, 209k messages and 21k attachments: 13.6 ms warm through `messages`
  against 0.36 ms through the column, because the join reads every message in
  the session *and* every attachment in the database to return a few hundred.
  Denormalising on a guess about load is suspicious; denormalising because the
  schema cannot express the access path is not.
- **`attachments.key_version` exists before rotation does.** A read has to know
  which key sealed an object, and adding the column now costs nothing while
  adding it later means a migration over every attachment ever stored. Every
  row is version 1 today.
- **What Postgres cannot include in a transaction gets a tombstone, not a
  best-effort call.** A cascade does not reach a bucket or a secret store, and
  the two writes can never both be atomic. What is atomic is recording the
  intent: a trigger writes a row to `deleted_objects` or `deleted_secrets` in
  the same transaction that removes the row it belonged to, and `brain reap`
  converges the other system afterwards. A transactional outbox, which makes an
  erasure something that has been *recorded* rather than something that was
  attempted. `internal/reaper` is the loop; each destination is an `Effect`.
- **The tombstone is written by a trigger, because a cascade never runs our
  SQL.** Deleting a team removes its attachments through channels, sessions and
  messages without any Go code seeing a row — and deleting a team is the
  commonest reason an erasure is owed. An outbox written by our own delete
  statements would catch nothing. The triggers are statement-level with
  transition tables, so deleting a team with ten thousand attachments is one
  insert rather than ten thousand invocations.
- **The effect happens first, the tombstone is cleared second.** A crash
  between them repeats the effect, which every effect tolerates: deleting an
  absent object or an absent secret is a no-op everywhere. Clearing first and
  crashing loses the record while the data survives, which is the original bug
  with extra steps. Destroy the record of work last.
- **A tombstone holds no personal data and has no foreign key.** Two uuids and
  a path. A filename in there would make it a list of the files people asked to
  have deleted, kept because they asked. A reference to `teams` would cascade
  away the record of the work still to do, at the exact moment the work is
  owed.
- **The alarm is the age of the oldest tombstone, never the count.** Nine
  hundred a minute old is a busy delete; one a day old is a sweep failing every
  run. Nothing else about a deleted row looks different depending on whether
  the other system caught up, so this is the only signal there is — and `brain
  reap` exits non-zero on it, because a failed job is seen and a log line is
  not.
- **A secret is destroyed by the sweep, not by the handler that deleted the
  row.** `DELETE /teams/{id}/channels/{channelID}` used to delete the signing
  secret itself and log when it failed, which left a live credential behind
  with a log line in front of it — and left every channel's secret behind when
  a *team* was deleted, because a cascade never reaches a handler.
- **Destroying a team's key is the half of an erasure that does not wait.**
  Secrets sweep before objects: once `teams/<id>/attachments` is gone every
  object of that team is undecryptable, including copies in bucket backups no
  sweep can reach. In OpenBao this is a delete of the *metadata* path — the
  data path only hides the latest version, and a secret somebody can undelete
  is a secret that was not erased.
- **`attachments.team_id` exists because a trigger cannot join for it.**
  Measured: during a team cascade the `sessions` row is already gone when the
  attachments trigger fires, so the tombstone would lose the one field the
  sweep cannot work without. Third copy of that fact and the same mechanism as
  the other two — a composite foreign key onto `sessions (id, team_id)` means
  it cannot drift.
- **Two tombstone tables, not one with a `kind` column.** They carry different
  columns today and would carry different constraints tomorrow, and a shared
  table forces the stricter of two sets of rules onto both. The *loop* is
  shared; the tables are not.
- **The sweep has no ordering and must not grow one.** `FOR UPDATE SKIP LOCKED`
  hands different rows to different sweepers in any order, which is safe only
  because deletes commute. An outbox for something that does not commute —
  outbound replies to one thread — needs a partition key and one worker per
  partition, and would be a second mechanism sharing a word rather than a third
  `Effect`.
- **The tombstones are an outbox, not a queue, and nothing may be built on them
  as if they were.** A queue row means "run this job with these arguments"; a
  tombstone means "Postgres has committed a change another system has not caught
  up to yet." `deleted_objects` holds a key, a team id, a timestamp and an
  attempt count — no payload, no job type, no dead-letter table, no routing, one
  consumer forever. That is exactly what makes replay free and `SKIP LOCKED`
  safe, and exactly why an agent runtime cannot sit on it. When one arrives it
  needs a real queue with two properties: it enqueues inside the transaction
  that writes the message, and a crash mid-run does not re-pay the tokens. No
  Postgres job queue has the second; Temporal cannot have the first, because
  starting a workflow is an RPC rather than a row — it sits behind an outbox
  rather than replacing one. `deployment/SCHEDULING.md` carries the measurements.
- **The scheduler is a seam because scheduling has no specification.** Auth is a
  port over OIDC, so authentik is swappable for Dex or Okta by changing one
  variable. River, Temporal, DBOS and Restate agree on no model at all, and an
  interface all four satisfy collapses to "run this function" — which discards
  the replay that was the reason to want Temporal. So the adapters are ours, on
  the `internal/gateway` pattern, and the heavy option is never mandatory: a
  2 GB host runs a ticker, a cluster runs what it already operates.
- **`MaxAttachment` is a constant, and not only for politeness.** `gcm.Seal`
  panics rather than erroring above `(2^32-2)*16` bytes, and an attachment is
  held whole in memory twice while it is sealed. A test asserts the constant
  stays under both ceilings, so raising it carelessly fails instead of
  crashing. Promoting it to configuration would need a validated ceiling.
- **The policy grants `update` on the attachment key path, and that is not
  redundant next to `create`.** Without it the loser of that race is refused by
  the policy (403) rather than by check-and-set (400), and cannot tell "somebody
  beat me" from "the policy is wrong". A root token returns 400 either way, so
  no test using a convenient credential can see this — the policy test builds
  its AppRole from the file that ships.
- **An attachment has no permission of its own.** It is reached through the
  session that holds it and uses that session's check — the same `visible` the
  message route uses. An `attachment:read` permission could be granted to a
  role without `session:read`, and then somebody who cannot open a private
  conversation could still read the file sent in it. Deriving the check makes
  that combination unrepresentable rather than merely unconfigured. The rule
  generalises: a permission for a child resource is a bug when the child is
  only reachable through its parent.
- **The read path serves the recorded type and nothing else.** `Content-Type`
  is `attachments.media_type` exactly, with `X-Content-Type-Options: nosniff`,
  never a type derived from the filename. Sniffing at write time is a claim;
  this is what makes it true. An SVG carrying a script sniffs as `text/plain`
  and is inert only while it is served as that.
- **`Content-Disposition: inline` is an allow list, and PDF is not on it.**
  Only `image/png`, `image/jpeg`, `image/gif`, `image/webp` and `text/plain`
  render in place, because each is inert as itself under nosniff. A same-origin
  PDF runs JavaScript in every major viewer, and a zip gains nothing from
  opening. Everything else downloads.
- **The brain proxies the bytes; there is no signed URL.** The object store
  holds ciphertext under a key it does not have, so a URL to it would serve
  something unreadable. Decryption happens in the process that already knows
  which team the caller was allowed to read, and the team comes from the
  session rather than from the object key — a string on a row.
- **A refusal has to do nothing, not merely answer nothing.** `visible` writes
  its 404 and returns; a handler that ignored its second return value would
  still answer 404, because the status is already committed and `WriteHeader`
  only fires once. The assertion that catches it is that the queries never ran.
  Status codes cannot detect work done after a refusal.
- **A write resolves the name it was given; it does not send the caller to a
  directory first.** `POST /teams/{id}/members` takes `user_id` or `subject`,
  because requiring the id would mean requiring `GET /users`, which needs
  `user:read` *somewhere* — a much larger authority than adding one person you
  can already name. The question to ask of any reference in a
  request body is what permission the caller needs to produce it.
- **Permissions are data; scopes are code.** Adding a permission, creating a
  role, or pointing a route at a different permission is rows, not a deploy —
  which is what lets an enterprise compose roles in a dashboard. Adding a
  *scope* is code, because each one is a different check.
  `internal/store/rbac.json` is the product's default catalogue, applied by
  `brain migrate up`: permissions are upserted and never deleted, only the
  roles named in the file are touched, and routes are replaced wholesale.
  `role_permissions.action` and `route_permissions.permission` are foreign keys
  onto `permissions`, so a typo is a rejected write rather than a grant that
  silently means nothing.
- **An adapter names attachments; it never fetches them.** `Parse` is pure and
  offline, so a payload that carries a file carries a `Ref` and the claimed
  filename, type and size — every one of them attacker-chosen, which is why
  they are spelled `Claimed`. Downloading needs a credential and a network
  call, so it happens afterwards through the optional `Fetcher` interface,
  after the handler has already resolved `Keys()`. Optional because a provider
  with no concept of an attachment has nothing to implement, and a stub
  returning `nil, nil` is indistinguishable from an empty file.
- **`FetchAttachment` is given the request, not only the ref.** The obvious
  signature takes a ref and a credential, which is enough for Slack — its refs
  are file ids you hand back to its API. It is not enough for a provider whose
  bytes arrive inside the delivery itself, where the ref means nothing without
  the body it came in. That only surfaced because `custom` ships: three
  adapters that all download from a server would have agreed on the narrower
  signature and been wrong together.
- **A ref is unique within a delivery, and the suite enforces it.**
  `FetchAttachment` is given a ref and nothing else, so two attachments sharing
  one means the second resolves to the first one's bytes and is stored under
  the second one's filename and media type. That ingest returns success and
  nothing downstream can tell. Both halves were individually correct — `Parse`
  produced valid refs, `FetchAttachment` resolved one it was handed — so the
  contract between them had to be written down before anything could check it.
  In `custom` the collision has two routes: two equal ids, and an id equal to
  another attachment's `#<index>` fallback, so the check is on the computed ref
  rather than on the id.
- **Attachments are fetched inline, and the ack window is what sizes the
  budget.** Two seconds for a delivery and one for a file, because Slack allows
  three to answer a webhook — not because that is what a download needs. Inline
  is then safe by construction: it cannot blow an ack. A file that does not fit
  is dropped with a log line, and that line is the trigger for building the
  queue rather than for raising the constant. Ack-first is the better design
  and needs a durable queue; building one to store a photo is the wrong order.
- **A failed fetch loses the file; a failed row loses the request.** A file
  that 404s would 404 on every retry, so the message is stored without it and
  the provider gets a 200. A row that will not write happens *after* the bytes
  reached the bucket, so it is a 500 — a 200 there leaves an object nothing
  names and a message nobody has.
- **`InsertMessage` returns an id and `ErrNoRows` means replay.** Attachments
  hang off a message id, and the obvious way to always get one back —
  `ON CONFLICT DO UPDATE SET external_id = EXCLUDED.external_id` — writes a new
  row version even when nothing changed: 200 replays of one row took the heap
  from 8 kB to 16 kB on 18.6. Replays are the hot case on a webhook. `DO
  NOTHING ... RETURNING id` gives the id when there is work and nothing when
  there is not, which is exactly the question the caller is asking.
- **The object key is a fresh id, never a hash of the content.** Content
  addressing would let identical files share an object, which is what
  `CountAttachmentsByObjectKey` anticipates — but it also tells anyone who can
  list the bucket that two teams hold the same file, and lets them confirm a
  guessed file is present by hashing it. That is the property the encryption
  exists to remove.
- **A filename is cleaned where the sender is not trusted, which is always.**
  Directory components are stripped, control characters become spaces, and the
  result is clipped to the 512 the column allows. The name is only ever echoed
  in a `Content-Disposition`, but a value that still looks like a path invites
  the first caller that joins it onto one.
- **A channel adapter receives values, never capabilities.** It gets a
  `gateway.WebhookRequest` rather than the `*http.Request`, `Credentials`
  rather than `secrets.Store`, and a `ReceivedAt` rather than the clock. So it
  cannot hijack the connection, read another channel's signing secret, or be
  untestable. Adapters are the code most likely to be wrong and least likely to
  be reviewed closely, and they sit on a public unauthenticated URL — what is
  left after taking the authority away is a function from bytes to structs,
  which can be tested exhaustively without being trusted. `Keys()` is the
  mechanism: the adapter declares which secrets it needs and the handler
  fetches them, scoped to the channel in the URL. See
  `internal/gateway/CLAUDE.md`.
- **A channel has sessions; a session has messages.** A session is one
  conversation, and the adapter chooses what that means through `Session.Ref` —
  a provider gives you identity, never episode. It belongs to a *team* rather
  than to a channel, because a channel is only one way a session starts and
  because a workspace or a sandbox will hang off it. `sessions` carries
  `status` and `last_message_at` that nothing writes yet: a Slack thread ends
  on its own, a direct message never does, and an idle sweep is the only thing
  that can tell them apart. The partial unique index is what lets that land as
  an UPDATE rather than a redesign. See `internal/gateway/CLAUDE.md`.
- **A direct session belongs to one person; the check is a permission, not a
  role.** `group` and `thread` sessions are the team's, `direct` is its
  participant's, and `session:read_all` is what lets anyone else read one. The
  handler asks `Can` for that permission rather than testing for `"admin"`,
  because which role holds it is data in `rbac.json` — a role name in Go would
  put that mapping back into a deploy.

  This is the first row-level check in the API: the guard authorises reaching
  the route, and the handler decides which rows come back. The two lists filter
  in SQL, and the single-session read filters in Go, so both have to agree —
  which is why the mutation pass breaks each independently.
- **The reference adapter ships; it is not a fake.** `internal/gateway/custom`
  is a real generic webhook anyone can integrate against, and it runs the same
  `providertest` suite as every other adapter. A test double is written by the
  same person, at the same time, with the same misunderstandings as the thing
  it validates, so it agrees by construction — a second real implementation is
  what proves an abstraction rather than the code.
- **The guard wraps every route; it is never a per-route decorator.**
  `Router.Register` takes an `api.RouteGuard` and there is no way to mount a
  handler without it. A decorator can be forgotten, and a forgotten one is an
  open endpoint that passes every test written about its body. A protected
  route with no row in `route_permissions` panics at startup naming the route;
  rows for routes nothing serves fail `serve` with the same specificity.
- **`Can` for a team, `CanInAnyTeam` for a route with no team in it.** The
  second is strictly weaker — an admin of one team passes it — so a
  team-scoped route using it would turn one admin role into an admin role
  everywhere. This is now a property of the *scope* rather than of the calling
  package: `any_team` on a route with `{id}` in its path is refused by a test
  in `internal/store`.
- **`member` is not `team` with a permission everyone holds.** Belonging is a
  fact and cannot be revoked or forgotten; a grant is configuration and can be
  both. Collapsing them would make "a member who cannot open their own team"
  reachable by omitting one line from a role in a dashboard. `member` also
  costs no query — it reads memberships already on the request — and denies
  with 404 rather than 403, because a team you do not belong to should not be
  confirmed to exist.
- **A subject is not unique.** `users` is unique on `(issuer, subject)`, so
  anything keyed on subject alone can match more than one person and must
  answer rather than pick. With one provider configured it never fires, which
  is exactly why it has to be handled when it is written rather than when it
  happens.
- **`slog` from the stdlib.** No zap, no zerolog. Text with source locations in
  development because a terminal is the reader; JSON everywhere else because a
  log aggregator is. `AddSource` costs a stack walk per record, so development
  only.
- **`/healthz` and `/readyz` are not logged.** Kubernetes probes them every ten
  seconds on two listeners — roughly 17k lines a day that say nothing and bury
  everything else. The skip lives in the middleware, matches the path exactly,
  and must still let the response through untouched.
- **Liveness never checks a dependency; readiness always does.** `/healthz`
  answers 200 with Postgres on fire — failing it restarts every pod at once,
  which fixes nothing and adds a reconnect storm to the outage. `/readyz` pings
  the database and returns 503, which takes the pod out of the Service and
  restarts nothing. Both routes go on both listeners: `kubelet` probes the pod
  IP, so the loopback API listener is unreachable to it and the webhook copies
  are the ones Kubernetes actually uses.
- **pgx, sqlc and goose. No ORM.** Three tools, one job each, none hiding the
  database. The queue needs `FOR UPDATE SKIP LOCKED`, the runtime tables need
  `jsonb` operators, and graph RAG will need CTEs — all natural in SQL and all
  awkward through an ORM. sqlc's weakness is dynamic queries; write the two or
  three real shapes as named queries rather than reaching for a builder.
- **`pgxpool` is lazy.** `New` dials nothing — a stopped database, a wrong host
  and a wrong password all return a working pool and a nil error. `Ping` is the
  only proof, which is why `run` calls it before anything else starts.
- **Pool settings live in code, not the DSN.** `pool_max_conns` and friends in
  a connection string are silently overridden by `applyPoolDefaults`. One place
  decides, and it is greppable. pgx's own default derives `MaxConns` from
  `NumCPU`, so the same image would open 8 connections on one node and 64 on
  another.
- **Migrations are embedded and applied by the binary.** `brain migrate up`,
  never the `goose` CLI against a real database — the CLI reads files from
  disk while the binary carries its own copy, and the two drift silently. In
  Kubernetes it is a Job that completes before the Deployment rolls: never an
  `initContainer` on every pod, and never inside `run`.
- **The migration advisory lock is project-specific.** goose's `DefaultLockID`
  is crc32 of the string `"goose"` and is therefore shared by every goose user;
  Postgres advisory locks are scoped per database, not per schema. Ours is
  crc32 of `"openarity"`. Locking is also off unless `WithSessionLocker` is
  passed — the default is no lock at all.
