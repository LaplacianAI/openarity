-- name: CreateSkill :one
INSERT INTO skills (team_id, name, description, body)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSkill :one
SELECT * FROM skills WHERE id = $1;

-- The body is left out: a page of fifty skills is fifty descriptions to choose
-- between, not fifty documents nobody asked to read.
-- name: ListSkillsByTeam :many
SELECT id, team_id, name, description, created_at, updated_at FROM skills
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: UpdateSkill :one
UPDATE skills SET
    name        = sqlc.arg('name'),
    description = sqlc.arg('description'),
    body        = sqlc.arg('body'),
    updated_at  = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteSkill :exec
DELETE FROM skills WHERE id = $1;
