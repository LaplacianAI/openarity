# The agent registry: storing agents, MCP servers and skills in the brain

**Status:** design, not built
**Module:** `apps/brain`

## The problem

`sdk/agent` runs an agent from a fully resolved `agent.Spec`, and decides
nothing about where that spec came from. The brain is meant to be the place it
comes from, and today it stores nothing an agent is made of: no agents table,
no tools, no skills.

This is the first of two steps.

1. **This one:** a team can create, read, change and delete the things an agent
   is made of. Nothing runs.
2. **Next:** the runtime. When a message reaches an agent, the brain resolves
   its rows into an `agent.Spec` — connecting its MCP servers, reading its
   skills' bodies, fetching secrets — and runs it. Default agents per team are
   seeded there.

Everything is instantiated on demand in step two. This step stores data only.

## Shaped by what the SDK can build

A row is only worth storing if the runtime can turn it into something the SDK
accepts. The SDK has three sources of what an agent can do:

| SDK type | What it needs | What a row can hold |
| --- | --- | --- |
| `agent.Spec` | model, pattern, system prompt, step limits, output schema, parser | all of it — plain data |
| `mcp.Server` | name, a URL **or** a command, env, `Bare` | all of it, with secrets as references |
| `agent.Skill` | name, description, `Body func(ctx)` | name, description, body text |
| `agent.Tool` | name, description, schema, `Invoke` closure | **nothing usable** — `Invoke` is code |

Two consequences:

- **There is no generic tools table.** A standalone `agent.Tool` is a Go
  closure, and the SDK ships none. A row describing one would be a row nothing
  can invoke. When the brain gains built-in tools (the HLD's user interaction,
  memory and learning tools), they are code with a catalogue, not CRUD.
- **An MCP server is the tool entity.** It is the only data-described tool
  source the SDK has. Its individual tools are not rows: `mcp.Connect` lists
  them live and prefixes each as `server__tool`. Storing them would copy the
  server's truth and go stale the day it adds one. An agent grants a server,
  optionally narrowed to an allow-list of tool names.

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
  id, team_id, name     -- name unique per team, and a valid tool-name prefix
  url                 text NULL
  command             text[] NULL -- CHECK exactly one of url, command
  env                 jsonb       -- {"VAR": "<secret path>"}, never a value
  auth_secret_ref     text NULL   -- secret path for the URL transport's header
  bare                bool        -- mcp.Server.Bare: no server__ prefix
  created_at, updated_at

skills
  id, team_id, name     -- unique per team
  description         text
  body                text
  created_at, updated_at

agent_mcp_servers
  agent_id → agents ON DELETE CASCADE
  mcp_server_id → mcp_servers ON DELETE RESTRICT
  allow               text[] NULL -- NULL grants every tool on the server
  PK (agent_id, mcp_server_id)

agent_skills
  agent_id → agents ON DELETE CASCADE
  skill_id → skills ON DELETE RESTRICT
  PK (agent_id, skill_id)
```

### Decisions

- **Secrets are references.** `env` values and `auth_secret_ref` are paths into
  the secret store. The runtime resolves them; the API never sees a value.
- **The stdio transport is stored, not yet trusted.** The SDK supports
  `Command`, so the row does. Whether the runtime spawns a process on the
  brain's host waits on the sandbox decision (`Agent-SDK.md`, open question 3).
- **`kind` separates agents the team wrote from agents the brain provides.** The
  API creates `custom` only. The others are seeded by the runtime step. The HLD
  names `orchestrator` as a brain package rather than an agent; it is a kind
  here so the default entry agent has a row a team can inspect and tune.
- **`pattern` stores every `PatternName`**, including `code` and `custom`. The
  runtime refuses one it has no pattern registered for; the registry does not
  guess which patterns a given brain was built with.
- **The API mirrors the SDK's own validation** so a bad agent is refused when it
  is written rather than when it first runs: `parser` needs `output_schema`,
  the schema's `json` must be valid JSON with a `name`, and an MCP server's
  name must match `^[a-zA-Z0-9_-]{1,64}$` once prefixed.
- **No versioning.** Nothing in the design asks for it. `updated_at` only.
- **Fixed string sets are defined Go types**, and `CHECK` constraints in the
  migration, following `team_members.role`.

## API

```text
GET    /teams/{id}/agents                 member
POST   /teams/{id}/agents                 team, agent:write
GET    /teams/{id}/agents/{agentID}       member
PUT    /teams/{id}/agents/{agentID}       team, agent:write
DELETE /teams/{id}/agents/{agentID}       team, agent:write

/teams/{id}/mcp-servers[/{serverID}]      same five, tool:write
/teams/{id}/skills[/{skillID}]            same five, skill:write
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
  skill; a list of twenty long bodies is a page nobody asked for.
- Everything else follows `channels`: handler straight to sqlc through a
  narrow `Store` interface, wire structs in `schema.go`, cursor pagination,
  404 for a row of another team, 409 on a duplicate name.

### Permissions

`agent:write` and `tool:write` already exist and are granted to both `admin`
and `member`. `skill:write` is new, granted to the same two roles. All three are
rows in `rbac.json`.

`Auth.md` splits `agent:invoke` from `agent:configure`. `agent:write` is
`agent:configure` under its existing name; `agent:invoke` arrives with the
runtime, which is the first thing that invokes.

## Testing

- **Query tests** against a real Postgres (`BRAIN_TEST_POSTGRES_DSN`), one file
  per table: the constraints refuse what they claim to (both transports, none,
  a parser with no schema, a duplicate name in any case), cascade and restrict
  behave on delete, and a `PUT` that fails part-way leaves the old grants.
- **Handler tests** per route, as `add-route` requires, through the real Guard:
  the permission is needed, a row of another team is a 404, a grant naming
  another team's server is a 400 that wrote nothing, a delete of a granted
  skill is a 409 that deleted nothing.
- **Mutation step:** each new guard is broken once to see a test fail.
- `spec_test.go` keeps routes and `openapi.yaml` in step; the CLI client is
  regenerated in the same change.

## Not in scope

- Running anything: resolving rows into an `agent.Spec`, connecting MCP
  servers, reading secrets, the model endpoint. That is the runtime step.
- Seeding default agents per team.
- Built-in Go tools and their catalogue.
- `agent:invoke`, and grants from users to agents.
- The FalkorDB projection (`TEAM-OWNS->AGENT`, `AGENT-GRANTED->Toolkit`,
  `AGENT-HAS_SKILL->Skill`). Postgres is truth; the index follows it.
- CLI commands (`oa agents …`), beyond regenerating the client.
