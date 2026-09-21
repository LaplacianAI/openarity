# The agent registry: agents, MCP servers and skills, and the graph they project into

**Status:** design, not built
**Module:** `apps/brain`

## The problem

`sdk/agent` runs an agent from a fully resolved `agent.Spec`, and decides
nothing about where that spec came from. The brain is meant to be the place it
comes from, and today it stores nothing an agent is made of: no agents table,
no tools, no skills. FalkorDB runs in every deployment and nothing talks to it
— `go.mod` has no client, and `deployment/README.md` calls it "running but
unused".

This change does two things:

1. A team can create, read, change and delete the things an agent is made of.
2. Every one of those rows is projected into FalkorDB, with embeddings, so the
   runtime has a graph to select from. The projection can be rebuilt from
   Postgres at any time, and CI proves it.

Running an agent — resolving rows into an `agent.Spec`, `search_tools`,
default agents per team — is the next change. Nothing runs here.

## Shaped by what the SDK can build

A row is only worth storing if the runtime can turn it into something the SDK
accepts. The SDK has three sources of what an agent can do:

| SDK type | What it needs | What a row can hold |
| --- | --- | --- |
| `agent.Spec` | model, pattern, system prompt, step limits, output schema, parser | all of it — plain data |
| `mcp.Server` | name, a URL **or** a command, env, `Bare` | all of it, with secrets as references |
| `agent.Skill` | name, description, `Body func(ctx)` | name, description, body text |
| `agent.Tool` | name, description, schema, `Invoke` closure | **nothing usable** — `Invoke` is code |

- **There is no generic tools table.** A standalone `agent.Tool` is a Go
  closure, and the SDK ships none. When the brain gains built-in tools (the
  HLD's user interaction, memory and learning tools), they are code with a
  catalogue, not CRUD.
- **An MCP server is the toolkit.** It is the only data-described tool source
  the SDK has, and it is the HLD's `Toolkit`: one connection, one credential,
  one lifecycle. Grants attach to it, optionally narrowed to an allow-list.
- **An MCP server's tools are rows too,** because the graph needs them:
  `Toolkit -CONTAINS-> Tool` is how selection finds siblings, and every graph
  node has a Postgres row behind it. They are discovered, never typed in — the
  brain connects with `mcp.Connect` and records what the server lists.

A skill's `Body` is a function so that it can load lazily. The body lives in a
Postgres column now; moving it to object storage later changes the runtime's
closure, not the API.

## Data model

```sql
agents
  id                  uuid PK
  team_id             uuid → teams ON DELETE CASCADE
  name                text      -- unique per team, case-insensitive
  description         text
  kind                text      -- custom | orchestrator | probing | memory | chat
  instructions        text      -- becomes agent.System(...)
  model_name          text
  max_tokens          int       -- > 0
  temperature         float8 NULL
  pattern             text      -- react | plan | rewoo | reflection | code | custom
  max_steps           int       -- > 0
  steer_continuations int       -- >= 0, default 0
  output_schema       jsonb NULL -- {name, description, json, strict}
  parser              bool      -- CHECK (NOT parser OR output_schema IS NOT NULL)
  parser_model_name   text NULL
  parser_max_tokens   int  NULL
  parser_temperature  float8 NULL
  created_at, updated_at

mcp_servers
  id, team_id, name     -- unique per team, and a valid tool-name prefix
  url                 text NULL
  command             text[] NULL -- CHECK exactly one of url, command
  env                 jsonb       -- {"VAR": "<path>#<key>"}, never a value
  auth_secret_ref     text NULL   -- <path>#<key> for the URL transport's header
  bare                bool        -- mcp.Server.Bare: no server__ prefix
  discovered_at       timestamptz NULL
  created_at, updated_at

mcp_tools
  id                  uuid PK
  mcp_server_id       uuid → mcp_servers ON DELETE CASCADE
  team_id             uuid        -- copied, composite FK with the server
  name                text        -- the remote name, as the server lists it
  description         text
  input_schema        jsonb
  UNIQUE (mcp_server_id, name)

skills
  id, team_id, name     -- unique per team
  description         text
  body                text
  created_at, updated_at

agent_mcp_servers
  agent_id, team_id → agents (id, team_id) ON DELETE CASCADE
  mcp_server_id, team_id → mcp_servers (id, team_id) ON DELETE NO ACTION
  allow               text[] NULL -- NULL grants every tool on the server
  PK (agent_id, mcp_server_id)

agent_skills
  agent_id, team_id → agents (id, team_id) ON DELETE CASCADE
  skill_id, team_id → skills (id, team_id) ON DELETE NO ACTION
  PK (agent_id, skill_id)

-- team_id on the link rows is forced by the triggers: during a team cascade
-- the agent is gone before its links' trigger fires, so the team has to be on
-- the row. The composite keys then make a cross-team grant impossible to write.
```

### Decisions

- **Secrets are references.** `env` values and `auth_secret_ref` are
  `path#key` references into the secret store — `secrets.Store.Get` takes both. Discovery resolves them; the API never sees a value.
- **The stdio transport is stored, not yet trusted.** The SDK supports
  `Command`, so the row does. Spawning a process on the brain's host waits on
  the sandbox decision (`Agent-SDK.md`, open question 3), so discovering a
  command server is refused with 409 until then.
- **`kind` separates agents the team wrote from agents the brain provides.** The
  API creates `custom` only; the others are seeded by the runtime change. The
  HLD names `orchestrator` as a brain package rather than an agent; it is a kind
  here so the default entry agent has a row a team can inspect and tune.
- **`pattern` stores every `PatternName`**, including `code` and `custom`. The
  runtime refuses one it has no pattern registered for.
- **The API mirrors the SDK's own validation** so a bad agent is refused when it
  is written rather than when it first runs: `parser` needs `output_schema`,
  the schema's `json` must be valid JSON with a `name`, and an MCP server's
  name must match `^[a-zA-Z0-9_-]{1,64}$` once prefixed.
- **No versioning.** Nothing in the design asks for it. `updated_at` only.
- **Fixed string sets are defined Go types**, and `CHECK` constraints in the
  migration, following `team_members.role`.

## API

```text
GET    /teams/{id}/agents                          member
POST   /teams/{id}/agents                          team, agent:write
GET    /teams/{id}/agents/{agentID}                member
PUT    /teams/{id}/agents/{agentID}                team, agent:write
DELETE /teams/{id}/agents/{agentID}                team, agent:write

/teams/{id}/mcp-servers[/{serverID}]               same five, tool:write
POST   /teams/{id}/mcp-servers/{serverID}/discover team, tool:write
GET    /teams/{id}/mcp-servers/{serverID}/tools    member

/teams/{id}/skills[/{skillID}]                     same five, skill:write
```

An agent's request and response carry its grants:

```json
{
  "name": "triage",
  "kind": "custom",
  "instructions": "You sort incoming issues.",
  "model": {"name": "anthropic/claude-opus-5", "max_tokens": 4096},
  "pattern": "react",
  "max_steps": 8,
  "mcp_servers": [{"id": "…", "allow": ["search_issues"]}],
  "skills": ["…"]
}
```

- **`PUT` replaces the whole agent, grants included**, in one transaction. A
  partial update is easy to add later and hard to take back. `kind` cannot
  change: a seeded agent can be tuned but not turned into `custom`, and a
  `custom` one cannot claim to be the orchestrator. A `PUT` with a different
  `kind` is 400.
- Every granted server and skill must belong to the same team; otherwise 400,
  and nothing is written.
- Deleting a server or skill that an agent still grants is **409**. A silent
  detach would change what an agent can do without anyone changing the agent.
- Skill bodies are omitted from list responses and returned by `GET` on one
  skill.
- Everything else follows `channels`: handler straight to sqlc through a
  narrow `Store` interface, wire structs in `schema.go`, cursor pagination,
  404 for a row of another team, 409 on a duplicate name.

### Discovery

`POST …/discover` connects to the server with `mcp.Connect`, lists its tools,
and replaces the server's `mcp_tools` rows in one transaction: new tools are
inserted, changed descriptions and schemas are updated, tools the server no
longer lists are deleted. `discovered_at` is set on success.

- It is explicit rather than done inside `POST /mcp-servers`, so creating a
  server never depends on the server being reachable, and a team can refresh
  after the server ships a new tool.
- An unreachable server is 502 and leaves the previous rows untouched.
- The request carries a timeout; the SDK's `Connect` is given the request's
  context.
- A command server is 409 until the sandbox question is answered.

### Permissions

`agent:write` and `tool:write` already exist and are granted to both `admin`
and `member`. `skill:write` is new, granted to the same two roles. All three are
rows in `rbac.json`.

`Auth.md` splits `agent:invoke` from `agent:configure`. `agent:write` is
`agent:configure` under its existing name; `agent:invoke` arrives with the
runtime, which is the first thing that invokes.

## The graph

*Postgres is truth; the graph is an index* (`HLD.md`). Nothing is written only
to FalkorDB, and a full rebuild is an ordinary, tested operation.

### One graph per team

Each team is its own FalkorDB graph, keyed `team:<uuid>`. Selection is always
team-scoped, so no query ever needs to cross one; a query that forgot its
`team_id` filter cannot leak another team's agents, because they are not in the
graph it is reading. Deleting a team is `GRAPH.DELETE`, and rebuilding one team
does not touch the others.

### What is projected

| Node | From | Properties | Embedded text |
| --- | --- | --- | --- |
| `Team` | teams | id | — |
| `Agent` | agents | id, name, description, kind | name + description |
| `Toolkit` | mcp_servers | id, name, kind `mcp` | — |
| `Tool` | mcp_tools | id, name, description | name + description |
| `Skill` | skills | id, name, description | name + description |

| Edge | From |
| --- | --- |
| `(Team)-[:OWNS]->(Agent)`, `(Team)-[:OWNS]->(Toolkit)`, `(Team)-[:OWNS]->(Skill)` | `team_id` |
| `(Agent)-[:GRANTED {only}]->(Toolkit)` | agent_mcp_servers; `only` is `allow` |
| `(Toolkit)-[:CONTAINS]->(Tool)` | mcp_tools |
| `(Agent)-[:HAS_SKILL]->(Skill)` | agent_skills |

A skill's body is not embedded. The description is what the SDK shows the
model when choosing a skill, so it is what selection should match against.

`Capability`, `Topic`, `Learning` and `SKILL -REQUIRES-> TOOL` are in the HLD's
graph and not here: nothing in this change writes the rows they would come
from.

Each embedded label gets a vector index (`Agent`, `Tool`, `Skill`, on
`embedding`) and a full-text index on `name` and `description`.

### Getting changes there: an outbox

Postgres and FalkorDB cannot share a transaction. What can be atomic is
recording that the graph is behind — the same transactional outbox the
tombstones use, for the same reason.

```sql
graph_outbox
  id          bigint GENERATED ALWAYS AS IDENTITY
  team_id     uuid
  entity      text    -- agent | mcp_server | mcp_tool | skill | team
  entity_id   uuid
  created_at  timestamptz
```

- **Triggers write it, not Go code.** `DELETE FROM teams` cascades through
  every table here without running a line of Go; a trigger is the only thing
  present at that moment. The tombstones migration measured the same problem.
- A row names what changed, not what it changed to. The projector reads the
  current Postgres state and makes the team's graph match it for that entity:
  upsert if the row exists, delete the node and its edges if it does not. That
  makes every outbox row idempotent and their order irrelevant.
- Link-table changes enqueue the agent they belong to.

### The projector

A DBOS job on the existing `worker`, alongside the reaper. It drains the outbox
in batches: coalesce by `(team_id, entity, entity_id)`, embed what needs
embedding, write to FalkorDB, delete the drained rows. A row is deleted only
after its write succeeded, so a crash replays rather than loses.

A graph that is behind is the alarm the tombstones already set the pattern for:
an outbox that stops shrinking is a projector that stopped.

### Embeddings

Brain-wide, over `OMNI_ROUTE_URL`'s OpenAI-compatible `/embeddings`:

```text
OPENARITY_EMBEDDING_MODEL       the gateway's name for it, e.g. ollama/nomic-embed-text
OPENARITY_EMBEDDING_DIMENSIONS  e.g. 768
OPENARITY_EMBEDDING_KEY_REF     path#key of the gateway key; empty for a
                                gateway that takes none. The key is never in config.
```

The brain only ever talks to the gateway. Where the gateway gets the embedding
from — a hosted provider, or the local Ollama below — is the gateway's
configuration, not the brain's.

### A local embedding model

`deployment/docker-compose.ollama.yml` is an optional overlay, in the same form
as the authentik, LiteLLM and OmniRoute ones:

- One `ollama` service, bound to `BIND_ADDR`, its models in a named volume, and
  a healthcheck.
- `make ollama` starts it and pulls `OLLAMA_EMBEDDING_MODEL` (default
  `nomic-embed-text`); `make ollama-down` stops it and keeps the volume.
- The gateway reaches it as `http://ollama:11434` on the compose network. The
  README section says how to register it as a provider in LiteLLM and in
  OmniRoute.
- The image is pinned to a version.

Nothing in the brain knows Ollama exists.

One model for every team means one vector dimension per index. Changing the
model is a rebuild, which is exactly the operation this design makes cheap.

Each embedded node carries what produced its vector — `model` and
`content_sha256` of the embedded text — as properties. The projector reads
them before embedding and skips the model call when both match, so editing an
agent's instructions, which are not embedded, costs nothing.

There is no Postgres copy of the vectors. They are derived like the rest of
the graph, and a rebuild — rare, since FalkorDB persists every write — simply
embeds again: seconds against a local Ollama.

### Rebuild

`brain graph rebuild [--team <id>]` is a standalone one-shot command, like
`brain reap`: drop the team's graph, recreate its indexes, project every row.
CI runs it against a seeded database and compares the result to what the
incremental projector produced from the same writes. The two agreeing is the
test that "rebuildable from Postgres" is true.

### FalkorDB in the brain

- The Go client is `github.com/FalkorDB/falkordb-go`, dialled from the existing
  `FALKOR_DB_URL`.
- The API does not depend on FalkorDB being up: it writes Postgres and the
  outbox. Only the worker and `brain graph rebuild` dial it, and the worker
  fails its start if it cannot.
- Compose pins `falkordb/falkordb` to a version instead of `latest`.

## Testing

- **Query tests** against a real Postgres (`BRAIN_TEST_POSTGRES_DSN`): the
  constraints refuse what they claim to (both transports, none, a parser with
  no schema, a duplicate name in any case), cascade and restrict behave on
  delete, a `PUT` that fails part-way leaves the old grants, and **every
  write — including a team cascade — leaves the outbox rows it should**.
- **Handler tests** per route, as `add-route` requires, through the real Guard:
  the permission is needed, a row of another team is a 404, a grant naming
  another team's server is a 400 that wrote nothing, a delete of a granted
  skill is a 409 that deleted nothing.
- **Discovery** against an in-process MCP server from the SDK's test kit: tools
  added, changed and removed; an unreachable server leaves the rows as they
  were.
- **Projector** against a real FalkorDB (`BRAIN_TEST_FALKOR_URL`, skipped when
  unset, like Postgres): each entity upserts and deletes; replaying an outbox
  row is a no-op; an unchanged text is not re-embedded (counted with a fake
  embeddings server); another team's graph is untouched.
- **Rebuild equals incremental**, as above.
- **Mutation step:** each new guard is broken once to see a test fail.
- `spec_test.go` keeps routes and `openapi.yaml` in step; the CLI client is
  regenerated in the same change.

## Not in scope

- Running anything: resolving rows into an `agent.Spec`, reading secrets for a
  run, the model endpoint for a run, `search_tools`. That is the runtime change.
- Seeding default agents per team.
- Built-in Go tools and their catalogue.
- `agent:invoke`, and grants from users to agents.
- Discovering command (stdio) MCP servers.
- Wiring Ollama into `start-docker.sh`'s questions; `make ollama` is enough
  until someone asks for it there.
- Learnings, capabilities, topics.
- CLI commands (`oa agents …`), beyond regenerating the client.
