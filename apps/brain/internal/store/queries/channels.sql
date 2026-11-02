-- name: CreateChannel :one
INSERT INTO channels (team_id, provider, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetChannel :one
-- A deleted team's channel answers as an unknown one, so its webhooks are
-- refused before the gateway fetches any attachment.
SELECT c.*
FROM channels c
JOIN teams t ON t.id = c.team_id
WHERE c.id = $1 AND t.deleted_at IS NULL;

-- name: DeleteChannel :exec
DELETE FROM channels WHERE id = $1;

-- name: ListChannelsByTeam :many
SELECT * FROM channels
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');
