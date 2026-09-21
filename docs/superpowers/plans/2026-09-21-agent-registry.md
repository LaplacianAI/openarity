# Agent registry Implementation Plan

> **For agentic workers:** this plan is executed under the working agreement,
> not subagent-driven development. **The user writes every file that is not a
> test** — Go, migrations, SQL, `rbac.json`, `openapi.yaml`, compose, CI —
> as `apps/brain/CLAUDE.md` requires. Claude hands each one over complete, one
> per reply, then reviews what was pasted and writes the tests that attack it. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A team can create, read, change and delete agents, MCP servers and
skills through the brain's API, and every one of those rows is projected, with
embeddings, into a per-team FalkorDB graph that can be rebuilt from Postgres.

**Architecture:** Three `internal/api` packages following `channels`
(handler → sqlc, no service layer). Triggers write a `graph_outbox` in the same
transaction as every change; a DBOS job in `brain worker` drains it through
`internal/graph`, which embeds via the gateway — skipping text whose hash is
already on the node — and writes FalkorDB. `brain graph rebuild` projects everything from
scratch, and CI proves it equals the incremental result.

**Tech Stack:** Go 1.26.6, pgx/v5, sqlc, goose, DBOS
(`dbos-transact-golang` v1.2.0), `github.com/FalkorDB/falkordb-go/v2` v2.1.0,
`github.com/LaplacianAI/openarity/sdk/agent` v0.1.0 (`tools/mcp`),
`github.com/modelcontextprotocol/go-sdk` (tests only, as the SDK's own tests
use it).

**Spec:** [`docs/superpowers/specs/2026-09-21-agent-registry-design.md`](../specs/2026-09-21-agent-registry-design.md)

## How this plan differs from the template

Tests are specified by **name and the exact behaviour asserted**, not as full
code. Claude writes them against the production file after it is pasted, the
way every test in `apps/brain` has been written: a test drafted before the
code it tests inherits whatever the plan guessed about signatures. The
production **interfaces** below are exact, because neighbouring tasks depend on
them.

## Global Constraints

- **No secret is ever stored in Postgres or the graph.** `env` values and
  `auth_secret_ref` are secret references, `path#key`. The embedding key is
  `EMBEDDING_KEY_REF`, also `path#key`.
- **RBAC is data.** Permissions and route rows go in
  `internal/store/rbac.json`; no Go change authorises anything.
- **A route with no `rbac.json` row panics at startup**, and
  `cmd/brain/spec_test.go` fails when a route and `api/openapi.yaml` disagree.
  Each API task adds its rows and its spec section in the same commit.
- **Never hand-write the Go that runs SQL** — queries in
  `internal/store/queries/*.sql`, then `make generate`.
- Migrations: `YYYYMMDDHHMMSS_<verb>_<thing>.sql`, start with
  `SET lock_timeout = '3s';`, carry a `-- +goose Down`.
- **Interfaces only where a second implementation exists.** Accept
  interfaces, return structs. A handler package declares a narrow `Store` of
  only the `db.*` methods it calls.
- A fixed set of strings is a defined Go type, and a `CHECK` in SQL.
- Log fields, not sentences. `httptest.NewRequestWithContext(t.Context(), …)`.
  Assert headers on `rec.Result().Header`.
- Errors: plain text via `http.Error`; unexpected ones through `api.Fail`.
  404 for another team's row, 409 on `23505`, 400 for a non-uuid path id.
- Every list is `{"items": [...], "next_cursor": "..."}`, cursor
  `{"c": created_at, "i": id}`, `?limit` default 50, max 100.
- Test names are sentences. Each new guard is broken once to see a test fail
  (`openarity-practices:verifying-what-you-build`).
- Gate before every commit: `cd apps/brain && make check db=postgres`.
  When the spec changes: `cd apps/cli && make generate && make check`.
- Commits: Conventional Commits, reader's side, **no attribution trailer**.

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/store/migrations/…_add_agent_registry.sql` | agents, mcp_servers, mcp_tools, skills, the two link tables |
| `internal/store/migrations/…_add_graph_outbox.sql` | graph_outbox and the triggers that fill it |
| `internal/store/queries/{agents,mcp_servers,mcp_tools,skills,graph_outbox}.sql` | sqlc queries |
| `internal/store/rbac.json` | `skill:write`, and every new route |
| `internal/api/skills/` | skills CRUD |
| `internal/api/mcpservers/` | MCP server CRUD, tools list, discover handler |
| `internal/discovery/discovery.go` | connect with `sdk/agent/tools/mcp`, return what the server lists |
| `internal/api/agents/` | agents CRUD with grants |
| `internal/config/config.go`, `validate.go` | embedding settings |
| `internal/graph/embed.go` | OpenAI-compatible `/embeddings` client |
| `internal/graph/falkor.go` | per-team graph: indexes, upsert, delete |
| `internal/graph/projector.go` | outbox → Postgres state → embed → FalkorDB |
| `internal/graph/job.go` | the DBOS job |
| `cmd/brain/graph.go`, `command.go`, `worker.go`, `routers.go` | wiring and `brain graph rebuild` |
| `api/openapi.yaml`, `apps/cli` generated client | the contract |
| `deployment/docker-compose.yml`, `docker-compose.ollama.yml`, `Makefile`, `README.md` | pinned FalkorDB, the Ollama overlay |
| `.github/workflows/ci.yml` | a FalkorDB service for the brain job |

Fifteen tasks. **Tasks 1–2 unblock everything; nothing reaches the graph
until Task 11.** Task 10 (CI) comes straight after the FalkorDB client, so
the graph tests stop skipping in CI before the projector is written.

---

### Task 1: The schema

**Files:**
- Create: `internal/store/migrations/<ts>_add_agent_registry.sql`
- Create: `internal/store/migrations/<ts>_add_graph_outbox.sql`
- Test: `internal/store/registry_schema_test.go`

Claude hands each migration over; the user creates it with
`make migration name=…` and pastes it in.

**Produces:** the tables exactly as in the spec's *Data model*, plus:

- `CHECK` names: `agents_kind_known`, `agents_pattern_known`,
  `agents_parser_needs_schema`, `agents_max_tokens_positive`,
  `agents_max_steps_positive`, `agents_steer_continuations_nonneg`,
  `mcp_servers_one_transport` (`(url IS NULL) <> (command IS NULL)`),
  `mcp_servers_name_is_prefix` (`name ~ '^[a-zA-Z0-9_-]{1,64}$'`),
  `*_name_present` on every name.
- Unique indexes `(team_id, lower(name))` on agents, mcp_servers, skills.
- `UNIQUE (id, team_id)` on agents, mcp_servers and skills, so children can
  carry a composite FK — the attachments pattern.
- `mcp_tools (mcp_server_id, team_id)` → `mcp_servers (id, team_id)`
  `ON DELETE CASCADE`: a tool cannot name a team its server is not in.
- **Link tables carry `team_id`**, because a trigger cannot see a parent above
  it in a cascade (`write-migration` step 5c). `(agent_id, team_id)` →
  agents `ON DELETE CASCADE`; `(mcp_server_id, team_id)` / `(skill_id,
  team_id)` → servers / skills `ON DELETE NO ACTION` (checked at end of statement, so a team cascade that removes the grants first is not refused mid-way). **This makes Postgres
  refuse a grant of another team's server or skill** (23503), so no query is
  needed to check it.
- `graph_outbox(id bigint identity, team_id uuid, entity text CHECK IN
  ('team','agent','mcp_server','mcp_tool','skill'), entity_id uuid,
  created_at)`. **No FK** — its rows must outlive what they name.
- **Statement-level triggers with transition tables**, never `FOR EACH ROW`.
  Postgres allows a transition table only on a single-event trigger, so each
  table gets three (insert, update, delete), every one naming its table
  `changed_rows`, sharing two functions:
  `graph_outbox_enqueue()` (`TG_ARGV[0]` is the entity; reads `team_id, id`)
  for agents, mcp_servers, mcp_tools, skills, teams (teams reads `id` as both),
  and `graph_outbox_enqueue_agent()` (reads `team_id, agent_id`) for the two
  link tables. Unverified in this shell — no Postgres; the schema tests are
  the proof.

- [ ] **Step 1:** Claude hands over the three migrations, one per reply; the user pastes each.
- [ ] **Step 2:** Claude writes `registry_schema_test.go`. Tests:
  - `TestAnMCPServerNeedsExactlyOneTransport` — url and command both set:
    23514; neither: 23514; each alone: inserted.
  - `TestAParserWithNoOutputSchemaIsRefused` — 23514, `agents_parser_needs_schema`.
  - `TestAgentNamesAreUniquePerTeamInAnyCase` — "Triage" then "triage" in one
    team: 23505; same name in another team: inserted.
  - `TestAnUnknownKindOrPatternIsRefused`.
  - `TestAServerNameThatCannotPrefixAToolIsRefused` — `"my server"`, 65 chars.
  - `TestAToolCannotNameATeamItsServerIsNotIn` — 23503.
  - `TestAGrantOfAnotherTeamsServerOrSkillIsRefused` — 23503 on both link tables.
  - `TestDeletingAGrantedSkillIsRefused` / `…GrantedServer…` — 23503, row still there.
  - `TestDeletingAnAgentDropsItsGrants`.
  - `TestEveryWriteLeavesAnOutboxRow` — table-driven over insert/update/delete
    of each entity and link: the expected `(entity, entity_id)` appears.
  - `TestDeletingATeamLeavesATeamOutboxRow` — and no trigger errors during the
    cascade (the tombstones failure mode).
- [ ] **Step 3:** `brain migrate up`, `down` ×3, `up` against a real Postgres; paste output.
- [ ] **Step 4:** `make check db=postgres`. Commit
  `feat(brain): the tables an agent is made of, and an outbox for the graph`.

### Task 2: Queries

**Files:**
- Create: `internal/store/queries/{agents,mcp_servers,mcp_tools,skills,graph_outbox}.sql`
- Test: `internal/store/{agents,mcp_servers,mcp_tools,skills,graph_outbox}_test.go`

Claude hands each query file over; the user pastes it; `make generate` writes the Go.

**Produces** (sqlc names, used by every later task):

```text
agents.sql       CreateAgent :one  GetAgent :one  ListAgentsByTeam :many (cursor)
                 UpdateAgent :one (all columns but id, team_id, kind; sets updated_at)
                 DeleteAgent :exec
                 AddAgentMCPServer :exec  ClearAgentMCPServers :exec  ListAgentMCPServers :many
                 AddAgentSkill :exec      ClearAgentSkills :exec      ListAgentSkills :many
mcp_servers.sql  CreateMCPServer :one  GetMCPServer :one  ListMCPServersByTeam :many
                 UpdateMCPServer :one  DeleteMCPServer :exec  MarkMCPServerDiscovered :exec
mcp_tools.sql    ListMCPToolsByServer :many  UpsertMCPTool :exec
                 DeleteMCPToolsNotIn :exec (server_id, names text[])
skills.sql       CreateSkill :one  GetSkill :one  ListSkillsByTeam :many (no body column)
                 UpdateSkill :one  DeleteSkill :exec
graph_outbox.sql ClaimGraphOutbox :many (oldest first, LIMIT, FOR UPDATE SKIP LOCKED)
                 ForgetGraphOutbox :exec (ids bigint[])  GraphOutboxBacklog :one
                 ListTeamIDs :many  -- for a full rebuild
```

- [ ] **Step 1:** User pastes the query files; `make generate`.
- [ ] **Step 2:** Claude writes query tests, each against a real Postgres:
  list pages by `(created_at, id)` with no row twice across pages;
  `ListSkillsByTeam` returns no body; `DeleteMCPToolsNotIn` with an empty
  array deletes all of that server's tools and none of another's;
  `ClaimGraphOutbox` in two concurrent transactions returns disjoint rows.
- [ ] **Step 3:** `make check db=postgres`. Commit
  `feat(brain): queries for agents, MCP servers, skills and the graph outbox`.

### Task 3: Skills API

**Files:**
- Create: `internal/api/skills/skills.go`, `schema.go` — **user writes**
- Modify: `cmd/brain/routers.go` — **user writes** one line
- Modify: `internal/store/rbac.json`, `api/openapi.yaml` — user
- Test: `internal/api/skills/skills_test.go`, `internal/store/rbac_test.go`

**Interfaces:**

```go
package skills

type Store interface {
	CreateSkill(ctx context.Context, arg db.CreateSkillParams) (db.Skill, error)
	GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error)
	ListSkillsByTeam(ctx context.Context, arg db.ListSkillsByTeamParams) ([]db.ListSkillsByTeamRow, error)
	UpdateSkill(ctx context.Context, arg db.UpdateSkillParams) (db.Skill, error)
	DeleteSkill(ctx context.Context, id uuid.UUID) error
}

func New(logger *slog.Logger, s Store) *api.Router
// GET/POST /teams/{id}/skills, GET/PUT/DELETE /teams/{id}/skills/{skillID}
```

Wire: `skill{id, team_id, name, description, body?, created_at, updated_at}` —
`body` present on create/get/put, absent in list. Request
`{name, description, body}`, all required. Limits: name 200 bytes via
`api.Name`, description 1536 (the SDK's listing limit — longer would be cut
silently), body 256 KiB. Delete of a granted skill: the FK's 23503 → **409**
"the skill is granted to an agent".

- [ ] **Step 1:** User adds `skill:write` (admin, member) and five route
  rows to `rbac.json`, pins them in `TestTheRouteMappingIsWhatWeIntend`, and
  writes the openapi section (`listSkills`, `createSkill`, `getSkill`,
  `replaceSkill`, `deleteSkill`, `SkillPage`, `Skill`, `SkillRequest`,
  `SkillID`).
- [ ] **Step 2:** User writes `schema.go`, then `skills.go`, then the
  `routers.go` line — one reply each.
- [ ] **Step 3:** Claude writes `skills_test.go`: the five tests per route from
  `add-route` step 6, plus body tests (malformed, missing field, wrong type,
  all 400 and store untouched), plus:
  `TestAListLeavesOutTheBody`, `TestAnotherTeamsSkillIsNotFound` (GET, PUT,
  DELETE), `TestDeletingAGrantedSkillIsAConflictThatDeletedNothing`,
  `TestADescriptionLongerThanTheListingLimitIsRefused`.
- [ ] **Step 4:** `add-route` step 7 mutation table, run and reported.
- [ ] **Step 5:** `make check db=postgres`; CLI regenerate. Commit
  `feat(brain): skills a team can write once and grant to agents`.

### Task 4: MCP server API

**Files:**
- Create: `internal/api/mcpservers/mcpservers.go`, `schema.go` — **user**
- Modify: `cmd/brain/routers.go` — **user**
- Modify: `rbac.json`, `openapi.yaml` — user
- Test: `internal/api/mcpservers/mcpservers_test.go`

**Interfaces:**

```go
package mcpservers

type Store interface {
	CreateMCPServer(ctx context.Context, arg db.CreateMCPServerParams) (db.McpServer, error)
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	ListMCPServersByTeam(ctx context.Context, arg db.ListMCPServersByTeamParams) ([]db.McpServer, error)
	UpdateMCPServer(ctx context.Context, arg db.UpdateMCPServerParams) (db.McpServer, error)
	DeleteMCPServer(ctx context.Context, id uuid.UUID) error
	ListMCPToolsByServer(ctx context.Context, serverID uuid.UUID) ([]db.McpTool, error)
}

func New(logger *slog.Logger, s Store) *api.Router
// Task 5 changes this to New(logger, s, d Discoverer) and adds the discover route.
```

Request `{name, url?, command?, env?, auth_secret_ref?, bare?}`. Validation in
the handler mirrors the CHECKs so the caller gets a sentence, not a 23514:
exactly one transport; `url` absolute http(s); name matches the prefix regex;
every `env` value and `auth_secret_ref` is a secret reference `path#key`
(`secrets.Store.Get` takes both) whose path is under this team's root —
a value that is not a reference is **400 "env values are secret
references, not secrets"**, because a pasted token would otherwise be
stored in Postgres.

Response includes `discovered_at` (null until Task 5).
`GET …/{serverID}/tools` → `{items:[{name, description, input_schema}]}`, not
paged (one server's tools are tens).

- [ ] **Step 1:** User: rbac rows (`tool:write` writes, `member` reads), openapi.
- [ ] **Step 2:** User: `schema.go`, `mcpservers.go`, routers line.
- [ ] **Step 3:** Claude tests: the `add-route` five per route, body tests, plus
  `TestBothTransportsIsRefused`, `TestNeitherTransportIsRefused`,
  `TestAnEnvValueThatIsNotASecretPathIsRefusedAndNotStored`,
  `TestASecretPathOfAnotherTeamIsRefused`,
  `TestDeletingAGrantedServerIsAConflictThatDeletedNothing`,
  `TestAnotherTeamsServerIsNotFound`, `TestToolsListsWhatWasDiscovered`.
- [ ] **Step 4:** Mutations. `make check`, CLI regenerate. Commit
  `feat(brain): MCP servers a team can register, with secrets by reference`.

### Task 5: Discovery

**Files:**
- Create: `internal/discovery/discovery.go` — **user**
- Create: `internal/api/mcpservers/discover.go` — **user**
- Modify: `apps/brain/go.mod` (require `sdk/agent v0.1.0`), `cmd/brain/routers.go`
- Test: `internal/discovery/discovery_test.go`, `internal/api/mcpservers/discover_test.go`

**Interfaces:**

```go
package discovery

type Tool struct {
	Name        string          // the remote name, never prefixed
	Description string
	InputSchema json.RawMessage
}

type Secrets interface {
	Get(ctx context.Context, path, key string) (string, error)
}

type Discoverer struct { /* secrets Secrets; client *http.Client; timeout time.Duration */ }

func New(sec Secrets, timeout time.Duration) *Discoverer

// Discover connects with mcp.Connect using Bare: true — so Tool.Name is the
// server's own name, not server__name — lists, closes, and returns.
// References are "path#key", split on the last '#'.
// A URL server with auth_secret_ref gets an http.Client whose transport sets
// Authorization: Bearer <value>. A command server returns ErrCommandServer
// without starting anything.
func (d *Discoverer) Discover(ctx context.Context, s db.McpServer) ([]Tool, error)

var ErrCommandServer = errors.New("command MCP servers cannot be discovered until the sandbox exists")
```

```go
package mcpservers

type Discoverer interface {
	Discover(ctx context.Context, s db.McpServer) ([]discovery.Tool, error)
}

// Store gains:
//   InTx(ctx context.Context, fn func(*db.Queries) error) error
// POST /teams/{id}/mcp-servers/{serverID}/discover:
//   ErrCommandServer → 409; any other Discover error → 502, rows untouched;
//   success → in one tx: UpsertMCPTool each, DeleteMCPToolsNotIn, MarkMCPServerDiscovered;
//   200 with the tools list body.
```

The handler's `Store.InTx` takes `func(*db.Queries) error`, matching
`*store.Store` — no adapter needed.

- [ ] **Step 1:** User writes `discovery.go`.
- [ ] **Step 2:** Claude writes `discovery_test.go` against a real MCP server
  over `httptest` built with `modelcontextprotocol/go-sdk` (the SDK's own
  `serve` helper pattern):
  `TestDiscoveryReturnsRemoteNamesNotPrefixedOnes`,
  `TestTheAuthSecretIsSentAsABearerToken` (server checks the header),
  `TestAMissingSecretFailsBeforeConnecting`,
  `TestACommandServerIsRefusedWithoutStartingAProcess`,
  `TestAnUnreachableServerIsAnError`, `TestDiscoveryHonoursTheTimeout`.
- [ ] **Step 3:** User writes `discover.go` and wires `discovery.New(secretStore, 15*time.Second)`.
- [ ] **Step 4:** User: rbac row, openapi `discoverMCPServer`. Claude:
  `discover_test.go`: `TestDiscoveryAddsChangesAndRemovesTools`,
  `TestAFailedDiscoveryLeavesThePreviousTools`,
  `TestACommandServerIsAConflict`, `TestAnotherTeamsServerCannotBeDiscovered`,
  plus the five.
- [ ] **Step 5:** Mutations, `make check`, CLI regenerate. Commit
  `feat(brain): discover an MCP server's tools, so the graph has something to contain`.

### Task 6: Agents API

**Files:**
- Create: `internal/api/agents/agents.go`, `schema.go`, `validate.go` — **user**
- Modify: `cmd/brain/routers.go` — **user**
- Modify: `rbac.json`, `openapi.yaml` — user
- Test: `internal/api/agents/agents_test.go`, `validate_test.go`

**Interfaces:**

```go
package agents

type Kind string    // custom | orchestrator | probing | memory | chat
type Pattern string // react | plan | rewoo | reflection | code | custom

type Store interface {
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	ListAgentsByTeam(ctx context.Context, arg db.ListAgentsByTeamParams) ([]db.Agent, error)
	ListAgentMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.AgentMcpServer, error)
	ListAgentSkills(ctx context.Context, agentID uuid.UUID) ([]uuid.UUID, error)
	DeleteAgent(ctx context.Context, id uuid.UUID) error
	InTx(ctx context.Context, fn func(*db.Queries) error) error
}

func New(logger *slog.Logger, s Store) *api.Router

// validate.go — pure, table-tested:
func validate(req agentRequest) error // first failure as a sentence for the 400
```

Create and replace run in `InTx`: write the agent row
(`CreateAgent`/`UpdateAgent`), `Clear*` then `Add*` for grants. A 23503 from
an `Add*` is **400 "a granted server or skill does not exist in this team"**
and the tx rolls back — the composite FK is the check. `kind` on create must be `custom` (400
otherwise); on replace must equal the stored kind (400). The response embeds
`mcp_servers:[{id, allow}]` and `skills:[id]`.

`validate` mirrors the SDK: `max_tokens > 0`, `max_steps > 0`,
`steer_continuations >= 0`, `temperature` in `[0, 2]` when set, `parser`
needs `output_schema`, `output_schema.json` is valid JSON and has a `name`,
`allow` entries non-empty and unique, no id granted twice.

- [ ] **Step 1:** User: rbac rows (`agent:write`), openapi (`Agent`,
  `AgentRequest`, `ModelRef`, `OutputSchema`, `MCPServerGrant`, `AgentPage`).
- [ ] **Step 2:** User writes `schema.go`, then `validate.go`.
- [ ] **Step 3:** Claude writes `validate_test.go` — one row per rule, each
  asserting the sentence.
- [ ] **Step 4:** User writes `agents.go`, then the routers line.
- [ ] **Step 5:** Claude writes `agents_test.go`: the five per route, body
  tests, and `TestCreatingANonCustomKindIsRefused`,
  `TestReplacingCannotChangeTheKind`,
  `TestAGrantOfAnotherTeamsServerIsRefusedAndNothingWasWritten`,
  `TestReplaceSwapsTheGrantsWhole`, `TestTheResponseCarriesTheGrants`,
  `TestAnotherTeamsAgentIsNotFound`. Plus one store test against Postgres:
  `TestAReplaceThatFailsPartWayKeepsTheOldGrants`.
- [ ] **Step 6:** Mutations, `make check`, CLI regenerate. Commit
  `feat(brain): agents a team can define, with the servers and skills they are granted`.

### Task 7: Embedding configuration

**Files:**
- Modify: `internal/config/config.go`, `validate.go` — **user**
- Modify: `deployment/docker-compose.yml`, `deployment/k8s/configmap.yaml`, `.env` example in README — user
- Test: `internal/config/validate_test.go`

Follow `add-env-var`.

```go
EmbeddingModel      string `env:"EMBEDDING_MODEL" envDefault:""`
EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" envDefault:"0"`
EmbeddingKeyRef     string `env:"EMBEDDING_KEY_REF" envDefault:""`
```

Validation: model and dimensions are both set or both empty (empty means the
worker runs no projector and says so at start, rather than failing every
install that has not chosen a model); dimensions in `1..4096`.

- [ ] **Step 1:** User edits config. **Step 2:** Claude tests: both empty ok,
  model without dimensions refused, dimensions out of range refused, the key ref
  is redacted in `String()` like every other reference is shown.
- [ ] **Step 3:** `make check`. Commit `feat(brain): settings for the embedding model`.

### Task 8: Embeddings client

**Files:**
- Create: `internal/graph/embed.go` — **user**
- Test: `internal/graph/embed_test.go`

```go
package graph

type Embedder struct { /* baseURL, model string; dims int; key string; client *http.Client */ }

func NewEmbedder(baseURL, model string, dims int, key string, client *http.Client) *Embedder

// Embed POSTs {model, input: texts} to baseURL+"/embeddings" in one request,
// returns vectors in input order (by the response's index field, not array
// position). A vector whose length is not dims is an error naming the model —
// the index was built for dims and would reject it later, less clearly.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error)

func (e *Embedder) Model() string
```

- [ ] **Step 1:** User writes `embed.go`.
- [ ] **Step 2:** Claude tests against `httptest`: order follows `index` when
  the server answers out of order; the key is sent as a bearer and omitted
  when empty; wrong dimension is an error naming model and both numbers; a
  non-200 includes the gateway's message; an empty input makes no request.
- [ ] **Step 3:** `make check`. Commit `feat(brain): embed through the model gateway`.

### Task 9: FalkorDB client

**Files:**
- Create: `internal/graph/falkor.go` — **user**
- Modify: `go.mod` (`github.com/FalkorDB/falkordb-go/v2 v2.1.0`)
- Test: `internal/graph/falkor_test.go`, `internal/graph/integration_test.go`

```go
type Graph struct { /* db *falkordb.FalkorDB; dims int */ }

func Dial(url string, dims int) (*Graph, error)
func (g *Graph) Ping(ctx context.Context) error
func (g *Graph) Close() error

func graphName(team uuid.UUID) string // "team:" + team.String()

// EnsureIndexes creates, if absent: vector indexes on Agent, Tool, Skill
// `embedding` (dims, cosine) and full-text on name, description.
func (g *Graph) EnsureIndexes(ctx context.Context, team uuid.UUID) error

type Node struct {
	Label     string          // Team | Agent | Toolkit | Tool | Skill
	ID        uuid.UUID
	Props     map[string]any  // name, description, kind
	Embedding []float32       // nil: leave the stored vector as it is
	Model     string          // with ContentSHA256, what produced Embedding
	ContentSHA256 []byte
}

// Fingerprint reads model and content_sha256 off a node; found is false when
// the node does not exist yet.
func (g *Graph) Fingerprint(ctx context.Context, team uuid.UUID, label string, id uuid.UUID) (model string, sha []byte, found bool, err error)

type Edge struct {
	Type  string            // OWNS | GRANTED | CONTAINS | HAS_SKILL
	From  uuid.UUID
	To    uuid.UUID
	Props map[string]any    // GRANTED: only
}

// PutNode MERGEs by id and replaces props and embedding.
// SetOutEdges replaces every outgoing edge of `from` of the given types with
// edges, so a revoked grant disappears without a separate delete.
// DeleteNode DETACH DELETEs by id. DropTeam GRAPH.DELETEs; absent is not an error.
func (g *Graph) PutNode(ctx context.Context, team uuid.UUID, n Node) error
func (g *Graph) SetOutEdges(ctx context.Context, team uuid.UUID, from uuid.UUID, types []string, edges []Edge) error
func (g *Graph) DeleteNode(ctx context.Context, team uuid.UUID, id uuid.UUID) error
func (g *Graph) DropTeam(ctx context.Context, team uuid.UUID) error

// Snapshot returns every node and edge, sorted, for the rebuild-equals-incremental test.
func (g *Graph) Snapshot(ctx context.Context, team uuid.UUID) (Snapshot, error)
```

All Cypher is parameterised; labels and edge types come from constants, never
from a row.

- [ ] **Step 1:** User writes `falkor.go`.
- [ ] **Step 2:** Claude writes `integration_test.go` (`BRAIN_TEST_FALKOR_URL`,
  skip when unset, each test on a fresh random team id) and `falkor_test.go`:
  `TestPutNodeTwiceKeepsOneNode`, `TestSetOutEdgesRemovesARevokedGrant`,
  `TestDeleteNodeTakesItsEdges`, `TestPutNodeWithNoEmbeddingKeepsTheStoredOne`,
  `TestFingerprintOfAMissingNodeIsNotFound`, `TestATeamsGraphCannotSeeAnothers`,
  `TestEnsureIndexesTwiceIsFine`, `TestAVectorQueryFindsTheNearestSkill`
  (proves the index is usable, not just created), `TestDropTeamOfNoGraphIsNotAnError`.
- [ ] **Step 3:** `make check db=postgres` with Falkor running. Commit
  `feat(brain): a FalkorDB graph per team`.

### Task 10: CI and local FalkorDB for tests

**Files:** `.github/workflows/ci.yml`, `apps/brain/Makefile`, `deployment/docker-compose.yml` — user

- Brain job gains a `falkordb/falkordb:<pinned>` service and
  `BRAIN_TEST_FALKOR_URL=redis://localhost:6379`.
- `make check db=postgres` also accepts `falkor=<url>`; README says how.
- Compose pins the same version in place of `latest`.
- **FalkorDB persists every write before acknowledging it**: append-only file,
  `appendfsync always`, in compose and k8s. Redis's default is periodic
  snapshots, and a crash would drop writes the projector had already
  acknowledged by deleting their outbox rows — silent drift. Confirm the
  variable the `falkordb/falkordb` image reads for server arguments with a
  probe, then assert it in a test that restarts the container and finds the
  node.

- [ ] **Step 1:** User edits, from what Claude hands over. **Step 2:** Confirm in the CI log that the Task 9
  tests ran rather than skipped; paste the line. Commit
  `ci(brain): run the graph tests against a real FalkorDB`.

### Task 11: The projector

**Files:**
- Create: `internal/graph/projector.go` — **user**
- Test: `internal/graph/projector_test.go`

```go
type Store interface {
	ClaimGraphOutbox(ctx context.Context, limit int32) ([]db.GraphOutbox, error)
	ForgetGraphOutbox(ctx context.Context, ids []int64) error
	GetAgent(ctx context.Context, id uuid.UUID) (db.Agent, error)
	GetMCPServer(ctx context.Context, id uuid.UUID) (db.McpServer, error)
	GetMCPTool(ctx context.Context, id uuid.UUID) (db.McpTool, error)
	GetSkill(ctx context.Context, id uuid.UUID) (db.Skill, error)
	ListAgentMCPServers(ctx context.Context, agentID uuid.UUID) ([]db.AgentMcpServer, error)
	ListAgentSkills(ctx context.Context, agentID uuid.UUID) ([]uuid.UUID, error)
	ListMCPToolsByServer(ctx context.Context, serverID uuid.UUID) ([]db.McpTool, error)
}

type Projector struct { /* store Store; graph *Graph; embed *Embedder; logger */ }

func NewProjector(s Store, g *Graph, e *Embedder, logger *slog.Logger) *Projector

// Drain claims up to batch rows, coalesces by (team, entity, id), projects
// each, and forgets only the rows whose projection succeeded. Returns how
// many were projected; an error from one entity does not stop the others.
func (p *Projector) Drain(ctx context.Context, batch int32) (int, error)

// Project makes the team's graph match Postgres for one entity:
// row present → PutNode (+ OWNS from Team, + its out-edges);
// row gone → DeleteNode.
// entity "team" with the team gone → DropTeam.
func (p *Projector) Project(ctx context.Context, team uuid.UUID, entity string, id uuid.UUID) error

// embedding returns nil, false when the node already carries this model and
// sha256(text) — PutNode then keeps its vector — otherwise embeds.
func (p *Projector) embedding(ctx context.Context, team uuid.UUID, label string, id uuid.UUID, text string) ([]float32, bool, error)
```

Embedded text is `name + "\n" + description` for Agent, Tool, Skill (spec).
`GetMCPTool` is added to `mcp_tools.sql` in this task.

- [ ] **Step 1:** User adds `GetMCPTool`; `make generate`.
- [ ] **Step 2:** User writes `projector.go`.
- [ ] **Step 3:** Claude tests against real Postgres + FalkorDB with a fake
  embeddings server that counts calls:
  `TestEachEntityAppearsInTheGraph` (table: agent, server, tool, skill, with
  their edges), `TestADeletedRowLeavesTheGraph`,
  `TestReplayingAnOutboxRowChangesNothing`,
  `TestAnUnchangedDescriptionIsNotEmbeddedAgain` (count stays),
  `TestChangingOnlyInstructionsCostsNoEmbedding`,
  `TestAChangedModelReembeds`, `TestARevokedGrantLeavesTheGraph`,
  `TestAnAllowListReachesTheGrantedEdge`,
  `TestAFailedEmbeddingLeavesTheRowToRetry` (outbox row still present),
  `TestDeletingATeamDropsItsGraphAndNoOther`.
- [ ] **Step 4:** Mutations: skip `ForgetGraphOutbox` on success, forget on
  failure, drop the hash comparison — each must fail a named test.
- [ ] **Step 5:** `make check`. Commit
  `feat(brain): project agents, tools and skills into the graph`.

### Task 12: The job

**Files:**
- Create: `internal/graph/job.go` — **user**
- Modify: `cmd/brain/worker.go` — **user**
- Test: `internal/graph/job_test.go`, `cmd/brain/wiring_test.go`

Follow `add-a-background-job`.

```go
const (
	drainWorkflow = "openarity.graph.drain"   // stored state — never rename
	drainSchedule = "graph-drain"
	drainCron     = "*/10 * * * * *"
)

func DrainJob(p *Projector, logger *slog.Logger) drainJob
func (drainJob) Name() string // "graph.drain"
func (j drainJob) Register(d dbos.Context) ([]dbos.ScheduleSpec, error)
```

`runWorker`: when `EmbeddingModel` is empty, log once that the graph is not
being projected and register only the reaper; otherwise `graph.Dial`, `Ping`
(fail start on error), resolve `EmbeddingKeyRef` from the secret store, add
`graph.DrainJob`. Log the backlog each run (`GraphOutboxBacklog`).

- [ ] **Step 1:** User writes `job.go`, then the `worker.go` change.
- [ ] **Step 2:** Claude tests: workflow and schedule names pinned; a nil
  projector refuses to register; `TestTheWorkerRegistersTheDrainWhenAModelIsSet`
  and `…DoesNotWhenItIsNot` in `wiring_test.go`.
- [ ] **Step 3:** `make check`. Commit `feat(brain): keep the graph up to date in the worker`.

### Task 13: `brain graph rebuild`

**Files:**
- Create: `cmd/brain/graph.go` — **user**
- Modify: `cmd/brain/command.go`, `main.go` — **user**
- Create: `internal/graph/rebuild.go` — **user**
- Test: `cmd/brain/command_test.go`, `internal/graph/rebuild_test.go`

```go
// command.go
commandGraph commandName = "graph"
// parse: "graph rebuild" and "graph rebuild --team <uuid>"; anything else is
// errGraphUsage = errors.New("usage: brain graph rebuild [--team <id>]")
type command struct { name commandName; direction direction; team uuid.UUID }

// rebuild.go
// Rebuild drops the team's graph, ensures indexes, and projects every agent,
// server, tool and skill of the team. It does not touch the outbox: rows
// written during a rebuild are drained afterwards and are idempotent.
func (p *Projector) Rebuild(ctx context.Context, team uuid.UUID) error
```

`Rebuild` needs list-by-team queries for each entity without paging
(`ListAllAgentIDsByTeam` etc.), added in this task.

- [ ] **Step 1:** User adds the queries; `make generate`.
- [ ] **Step 2:** User writes `rebuild.go`, then `graph.go`, then `command.go`/`main.go`.
- [ ] **Step 3:** Claude tests: parse table (`graph`, `graph rebuild`,
  `graph rebuild --team x` bad uuid, extra args); and the one the spec calls
  the proof — `TestARebuildEqualsTheIncrementalGraph`: seed a team through the
  API-level queries (create, grant, revoke, rename, delete some), drain,
  `Snapshot`; `Rebuild`; `Snapshot` again; the two are equal. And
  `TestARebuildReembedsEveryNode` (the fake server's count equals the node count).
- [ ] **Step 4:** `make check`. Commit
  `feat(brain): rebuild a team's graph from Postgres`.

### Task 14: Ollama overlay

**Files:** `deployment/docker-compose.ollama.yml`, `deployment/Makefile`, `deployment/README.md` — user

- `ollama` service, pinned `ollama/ollama:<version>`, bound to
  `${BIND_ADDR:-127.0.0.1}:${OLLAMA_PORT:-11434}`, volume `ollama-data`,
  healthcheck `ollama list`.
- `make ollama`: up, wait healthy, `ollama pull ${OLLAMA_EMBEDDING_MODEL:-nomic-embed-text}`.
  `make ollama-down`: stop, keep volume. Listed in `down`/`ps`/`destroy` like
  the gateways are.
- README: registering `http://ollama:11434` in LiteLLM (`ollama/nomic-embed-text`)
  and in OmniRoute, and setting `EMBEDDING_MODEL`/`EMBEDDING_DIMENSIONS=768`.

- [ ] **Step 1:** User writes, from what Claude hands over. **Step 2:** The user runs `make ollama` and a
  `curl` to the gateway's `/v1/embeddings` on a machine with Docker; output
  pasted into the PR. Commit `feat(deployment): a local embedding model the gateway can route to`.

### Task 15: Documentation and the PR

**Files:** `apps/brain/README.md` / `CLAUDE.md` as `update-project-docs` directs,
`deployment/README.md` (FalkorDB no longer "unused"), openarity-ideation
`Known-Gaps.md` if a gap was found.

- [ ] **Step 1:** `update-project-docs`.
- [ ] **Step 2:** End-to-end on a Docker machine: `make up`, `make ollama`,
  create a skill, a server, discover, an agent granting both, run the worker,
  `GRAPH.QUERY team:<id> "MATCH (a:Agent)-[r]->(n) RETURN a.name, type(r), n.name"`;
  paste the output. Then `brain graph rebuild --team <id>` and the same query.
- [ ] **Step 3:** `open-a-pull-request`; the user pushes through GitHub Desktop.

---

## Self-review

- **Spec coverage:** data model → 1; queries → 2; skills/servers/agents API →
  3, 4, 6; discovery → 5; permissions → 3, 4, 6; one graph per team, nodes,
  edges, indexes → 9, 11; outbox and triggers → 1; projector → 11; embeddings
  config, client, cache → 7, 8, 11; rebuild and its CI proof → 13; FalkorDB in
  the brain, pinned → 9, 10, 12; Ollama → 14; testing section → each task.
- **Deliberately deferred per spec:** runtime, default-agent seeding, built-in
  tools, `agent:invoke`, stdio discovery, `start-docker.sh` Ollama question.
