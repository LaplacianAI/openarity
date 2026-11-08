# brain

The Openarity backend. One Go module, one binary, five packages drawn exactly
as services. Design lives in the
[openarity-ideation](https://github.com/LaplacianAI/openarity-ideation) repo,
under `Tech Design/HLD-V1/HLD.md`.

## Working agreement

Two tiers. The rules an agent needs are all in this section; Claude Code users
also have them as the `working-agreement` skill from the openarity-practices
plugin. **Tier 1, the user's:**
`internal/auth`, `authz`, `secrets`, `objects`, `runtime`, `gateway`, `purge`,
`reaper`, the store's Go and `cmd/brain`, and the decisions in any handler: who
may see what, which status a failure gets. The agent tests and attacks that
code, explains and reviews; it does not write it. **Tier 2, the agent's:**
generated code (`internal/store/db`), wire-shape plumbing in `internal/api/*`
(schema fields, view mappers, ripples of a signature), migrations, SQL
queries, `sqlc.yaml`, `api/openapi.yaml`, `rbac.json`, tests, docs and
Makefiles. Design first in both: a high-level design (the user flow first, then
the UML diagrams the change has: sequence, state, component, entity) and a
low-level design with its sequence diagram, approved before code; every edit handed over with a link to
its file and line.

- "Make the change yourself, be like me" is a *style* instruction (sparse
  comments, only where the why is not obvious), not permission to write tier 1
  code.
- For tier 1, guide step by step, one file per reply. Do not scaffold files or
  finish a function unless explicitly asked.
- When tier 1 code is pasted: review it, list what is missing or wrong, then
  write tests that attack it — not tests that confirm it works.
- Explanations stay short and plain, and always include a small snippet.
  Functions in snippets stay small: one job, few lines, no cleverness.
- Verify library behaviour with a throwaway probe under the scratchpad rather
  than asserting it from memory. Both `net.SplitHostPort` and `url.Parse`
  turned out far more lenient than they look.

## Skills

Read the skill before the work it names. Full
descriptions are in [DECISIONS.md](DECISIONS.md#skills-full-descriptions).

- **`add-env-var`** — any new environment variable (struct field, enum, validation, redaction, three test files).
- **`add-middleware`** — anything wrapping an `http.Handler`; chain order and wiring.
- **`authorise-a-route`** — permissions, scopes, roles, a 403 or 404; read before `internal/authz`.
- **`add-a-backend`** — a new secret or object store behind a port.
- **`update-api-spec`** — `api/openapi.yaml` on every endpoint change; use with `add-route`.
- **`add-route`** — every API endpoint and every new package under `internal/api`.
- **`add-a-background-job`** — scheduled or queued work; read before touching `brain worker`.
- **`write-migration`** — every schema change.
- **`write-query`** — every Postgres query; never hand-write the Go that runs SQL.
- **`write-tests`** — every test; read before a concurrency test.
- **`fix-lint`** — golangci-lint or gofumpt failures; the fix is almost never a `nolint`.
- **`test-with-postgres`** — any test needing a real database; read step 5 before a concurrency test.

## Layout

```text
apps/brain/
  cmd/brain/
    main.go            main(), run(), execute()
    command.go         argument parsing — pure, no dependencies
    serve.go           the serve role
    migrate.go         the migrate role
    reap.go            the reap role, and newEffects for both it and the worker
    worker.go          the worker role, and newJobs: every job it runs
    graph.go           the graph role: rebuild one team, or every team
    projector.go       newProjector, for the worker and the graph role
    importer.go        newImporter: where a skill may be imported from
    discoverer.go      newDiscoverer: what MCP discovery may reach
    runtime.go         newRunJob: the builder, the runner and the executor, for the worker
    replies.go         newReplyJob and the reply client: what a reply URL may reach
    websearch.go       newSearchers: a team's search provider, built from its settings
    webfetch.go        newFetch: the plain fetch, a team's Firecrawl, and the public-host check
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
    runs/              start, read, cancel and follow a run; list a team's runs; events.go is the stream
    loops/             the loops an agent may choose, which are the runner's
    teamsettings/      a team's model, web search, learning and compaction
    teamsummary/       the Home counts: one statement, one snapshot
  internal/gateway/    a provider's webhook in, normalised messages out; replies back out
    providertest/      the conformance suite every adapter runs
    <provider>/        one package per platform; custom is the reference
  internal/secrets/    the port: Store, Writer, Creator, Prober, secret paths
    datakeys.go        a team's data key, generated once and never replaced
    openbao/           the AppRole client — the only thing that reaches OpenBao
    static/            the in-process fallback, for a brain with no OpenBao
  internal/objects/    the port: Store, Writer, object keys and team prefixes
    encrypt.go         AES-256-GCM over any backend — a layer, not a backend
    sniff.go           what a file is, and whether it may be stored
    s3/                the S3 API — SeaweedFS, MinIO, Ceph, R2, GCS, AWS; also built for seaweedfs
    filesystem/        a volume, for a single-host deployment with no S3
    inmemory/          the in-process fallback; attachments die with the process
  internal/skill/      a skill as the Agent Skills specification lays it out
    hub/               importing one: GitHub, or a zip from an allowed host
  internal/discovery/  asking an MCP server for its tools, and what it may reach
  internal/graph/      FalkorDB as an index over Postgres
    falkor.go          the client: one graph per team, nodes, edges, indexes
    params.go          query parameters, written here rather than by the library
    embed.go           vectors from the model router
    search.go          the read side: what a caller may reach, ranked, with paths
    projector.go       the outbox in, the graph out
    rebuild.go         a team's graph from nothing
    job.go             the drain as background work
  internal/reaper/     the sweep: one loop, one Effect per outside system
    job.go             the reaper as background work: SweepAll, and its Job
  internal/purge/      erasing deleted teams once TEAM_PURGE_AFTER has passed
  internal/runtime/    a run: the agent built from its row, executed, recorded
    build.go           the Spec, its granted MCP tools and skills; Patterns and Loops
    delegate.go        delegate(agent, objective, output, boundaries): the checks, then a child run
    events.go          what a run does, as run_events, within its token budget
    execute.go         one run: claim, heartbeat, run to the end, finish
    graphtool.go       graph(query, kinds, hops), bound to every run
    hub.go             the hub: the built-in orchestrator, or the team's chosen agent, reaching the whole team
    job.go             the run queue, the sweep that takes over stale runs, the Enqueuer
    narrow.go          a child built with fewer than its grants, for the outfitter
    plan.go            plan(items), the hub's checklist, each change a run event
    report.go          a child's answer, wrapped as data for the parent to read
    websearch.go       web_search(query, limit) and webTools, the one settings read for both web tools
    webfetch.go        web_fetch(url), the page in an envelope, cut at 32 KiB
  internal/websearch/  the port: Provider, Hit, Searcher, and the one guarded client
    <provider>/        searxng, brave, tavily, firecrawl: each exports New
  internal/webfetch/   the port: Page, Fetcher, and a client that dials public addresses only
    plain/             the brain's own read: readability, then markdown
    firecrawl/         a team's Firecrawl: /v2/scrape
  internal/worker/     the background process: a registry of Jobs, and Run
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

Each rule's full reasoning is in [DECISIONS.md](DECISIONS.md#conventions-with-their-reasons).

- **Initialisms keep one case**: `URL`, `DSN`, `API`, `ID`, `DB`; unexported starts all-lowercase (`redactURL`, `urlParser`).
- **File names match content**: singular for one type, plural for several. Split by job, not by size.
- **A type, its constants and its methods stay together** in one file.
- **Interfaces only where a second implementation exists.** Accept interfaces, return structs.
- **No package until it has an occupant.** No `util`; name packages after what they do.
- **Tests mirror source files**: `validate.go` has `validate_test.go`.
- **Tests inject configuration, never mutate process env**: `load(map[...])`, not `t.Setenv`.
- **`os.Exit` appears only in `main`, and `main` holds no defers.** Cleanup lives in `run(ctx, out)`.
- **Take the context and the writer as parameters.**
- **Tests reserve a real ephemeral port**: bind `127.0.0.1:0`, read `l.Addr()`, close, reuse. Never hardcode, never port 0.
- **`noctx` bans context-free constructors**: use `httptest.NewRequestWithContext`, `(*net.ListenConfig).Listen`, `exec.CommandContext`; the context is `t.Context()`.
- **Lifecycle tests poll, they do not sleep.**
- **Use the stdlib type when it encodes the constraint** (`slog.Level`). Probe the stdlib before writing an enum.
- **The logger is a parameter, never a global.** A nil one panics rather than defaulting.
- **Log fields, not sentences.** Never log a whole struct.
- **Log `r.URL.Path`, never `r.URL.String()`.**
- **Middleware returns a `Middleware`, it does not take `next` directly.**
- **An exported identifier that nothing calls is invisible.** Only a test that drives the wired object catches it.
- **Validate what comes from outside; trust the composition root.** A nil logger is a panic, not a fallback.
- **Cheapest failure first.** Parse arguments, then load config, then dial Postgres.
- **Parsing is separate from execution.** `parse` is pure; `execute` validates nothing.
- **A fixed set of strings is a defined type.** Keep the `default:` arm, and `default-signifies-exhaustive: false`.
- **Never put a DSN in an error message.**
- **Never delete error handling to raise a coverage number.** Pin the assumption with a test; see `TestTheSessionLockerWeBuildCannotFail`.

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

## Decisions

The decisions worth not relitigating, each with its reason, are in [DECISIONS.md](DECISIONS.md). Read it before changing a design, a boundary, or anything a convention above protects.
Topics: team deletion, config, the graph, runs and the hub, web tools, secrets, attachments, tombstones, jobs, permissions, logging, Postgres.
