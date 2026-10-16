-- name: CreateMCPServer :one
INSERT INTO mcp_servers (team_id, name, url, command, env, auth_secret_ref, bare)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = $1;

-- name: ListMCPServersByTeam :many
SELECT * FROM mcp_servers
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- A server pointed somewhere else has not been discovered there, so a changed
-- transport clears discovered_at. The tools stay until the next discovery
-- replaces them: an agent granted this server keeps what it had rather than
-- losing every tool to an edit.
-- name: UpdateMCPServer :one
UPDATE mcp_servers SET
    name            = sqlc.arg('name'),
    url             = sqlc.narg('url'),
    command         = sqlc.narg('command'),
    env             = sqlc.arg('env'),
    auth_secret_ref = sqlc.narg('auth_secret_ref'),
    bare            = sqlc.arg('bare'),
    discovered_at   = CASE
        WHEN url IS NOT DISTINCT FROM sqlc.narg('url')
         AND command IS NOT DISTINCT FROM sqlc.narg('command')::text[]
        THEN discovered_at
    END,
    updated_at      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = $1;

-- Only the version that was discovered is marked: a server edited or deleted
-- while discovery ran matches no row, and the caller rolls its tools back.
-- The row lock this takes makes a concurrent edit wait for the commit.
-- name: MarkMCPServerDiscovered :execrows
UPDATE mcp_servers SET discovered_at = now()
WHERE id = sqlc.arg('id') AND updated_at = sqlc.arg('updated_at');
