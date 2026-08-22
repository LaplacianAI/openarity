-- name: CreateChannel :one
INSERT INTO channels (team_id, provider, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetChannel :one
SELECT * FROM channels WHERE id = $1;

-- name: DeleteChannel :exec
DELETE FROM channels WHERE id = $1;

-- name: ListChannelsByTeam :many
SELECT * FROM channels
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');
