-- name: CreateAgent :one
INSERT INTO agents (
    team_id, name, description, kind, instructions,
    model_name, max_tokens, temperature, pattern, max_steps, steer_continuations,
    output_schema, parser, parser_model_name, parser_max_tokens, parser_temperature,
    token_budget, delegation
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16,
    $17, $18
) RETURNING *;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: ListAgentsByTeam :many
SELECT * FROM agents
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- kind and team_id are absent on purpose: a replace can change neither.
-- name: UpdateAgent :one
UPDATE agents SET
    name                = sqlc.arg('name'),
    description         = sqlc.arg('description'),
    instructions        = sqlc.arg('instructions'),
    model_name          = sqlc.arg('model_name'),
    max_tokens          = sqlc.arg('max_tokens'),
    temperature         = sqlc.narg('temperature'),
    pattern             = sqlc.arg('pattern'),
    max_steps           = sqlc.arg('max_steps'),
    steer_continuations = sqlc.arg('steer_continuations'),
    output_schema       = sqlc.narg('output_schema'),
    parser              = sqlc.arg('parser'),
    parser_model_name   = sqlc.narg('parser_model_name'),
    parser_max_tokens   = sqlc.narg('parser_max_tokens'),
    parser_temperature  = sqlc.narg('parser_temperature'),
    token_budget        = sqlc.narg('token_budget'),
    delegation          = sqlc.arg('delegation'),
    updated_at          = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = $1;

-- name: AddAgentMCPServer :exec
INSERT INTO agent_mcp_servers (agent_id, mcp_server_id, team_id, allow)
VALUES ($1, $2, $3, $4);

-- name: ClearAgentMCPServers :exec
DELETE FROM agent_mcp_servers WHERE agent_id = $1;

-- name: ListAgentMCPServers :many
SELECT * FROM agent_mcp_servers WHERE agent_id = $1 ORDER BY mcp_server_id;

-- One statement for every skill: each grant is only ids, so there is nothing
-- per row that needs its own INSERT, and one statement is one outbox row.
-- name: AddAgentSkills :exec
INSERT INTO agent_skills (agent_id, skill_id, team_id)
SELECT sqlc.arg('agent_id'), unnest(sqlc.arg('skill_ids')::uuid[]), sqlc.arg('team_id');

-- name: ClearAgentSkills :exec
DELETE FROM agent_skills WHERE agent_id = $1;

-- name: ListAgentSkills :many
SELECT skill_id FROM agent_skills WHERE agent_id = $1 ORDER BY skill_id;

-- One statement for the whole list, as the skills are: one outbox row.
-- name: AddAgentDelegates :exec
INSERT INTO agent_delegates (agent_id, delegate_id, team_id)
SELECT sqlc.arg('agent_id'), unnest(sqlc.arg('delegate_ids')::uuid[]), sqlc.arg('team_id');

-- name: ClearAgentDelegates :exec
DELETE FROM agent_delegates WHERE agent_id = $1;

-- name: ListAgentDelegates :many
SELECT delegate_id FROM agent_delegates WHERE agent_id = $1 ORDER BY delegate_id;

-- A run names its agent; names are unique per team in any case, and
-- agents_team_name_key is on (team_id, lower(name)), so this reads one entry.
-- name: GetAgentByName :one
SELECT * FROM agents
WHERE team_id = sqlc.arg('team_id') AND lower(name) = lower(sqlc.arg('name')::text);

-- What a run binds, read when it starts: each granted server with the grant's
-- allow-list, which is null for every tool on the server.
-- name: ListGrantedMCPServers :many
SELECT sqlc.embed(s), g.allow
FROM agent_mcp_servers g
JOIN mcp_servers s ON s.id = g.mcp_server_id
WHERE g.agent_id = $1
ORDER BY s.name;

-- With the body, unlike ListSkillsByTeam: a run hands it to the model when
-- the skill is loaded. One table in the FROM, so sqlc returns db.Skill.
-- name: ListGrantedSkills :many
SELECT * FROM skills
WHERE id IN (SELECT skill_id FROM agent_skills WHERE agent_id = $1)
ORDER BY name;

-- What the graph tool may return as an agent: whom the caller may delegate
-- to. The rest of the team, only those it lists, or nobody; never itself.
-- name: ListReachableAgentIDs :many
SELECT a.id FROM agents a
JOIN agents caller ON caller.id = sqlc.arg('agent_id')::uuid AND caller.team_id = a.team_id
WHERE a.team_id = sqlc.arg('team_id')::uuid AND a.id <> caller.id
  AND (caller.delegation = 'team'
    OR (caller.delegation = 'listed' AND EXISTS (
          SELECT 1 FROM agent_delegates d
          WHERE d.agent_id = caller.id AND d.delegate_id = a.id)))
ORDER BY a.id;

-- What the graph tool may return as a tool: every tool on a server granted
-- to the agent, cut by the grant's allow-list where it has one.
-- name: ListGrantedToolIDs :many
SELECT t.id
FROM agent_mcp_servers g
JOIN mcp_tools t ON t.mcp_server_id = g.mcp_server_id
WHERE g.agent_id = $1 AND (g.allow IS NULL OR t.name = ANY (g.allow))
ORDER BY t.id;
