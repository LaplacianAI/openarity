-- name: CreateAgent :one
INSERT INTO agents (
    team_id, name, description, kind, instructions,
    model_name, max_tokens, temperature, pattern, max_steps, steer_continuations,
    output_schema, parser, parser_model_name, parser_max_tokens, parser_temperature
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16
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
