-- name: CreateSkill :one
INSERT INTO skills (
    team_id, name, description, license, compatibility, metadata, allowed_tools,
    body, source, source_ref, source_sha
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    $8, $9, $10, $11
) RETURNING *;

-- name: GetSkill :one
SELECT * FROM skills WHERE id = $1;

-- The body is left out: a page of fifty skills is fifty descriptions to choose
-- between, not fifty documents nobody asked to read. The origin stays in, so a
-- list shows at a glance which skills are the team's own.
-- name: ListSkillsByTeam :many
SELECT id, team_id, name, description, source, created_at, updated_at FROM skills
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- A replace is whole, the origin included: an upload over an imported skill
-- makes it the team's own, and a sync writes the new commit.
-- name: UpdateSkill :one
UPDATE skills SET
    name          = sqlc.arg('name'),
    description   = sqlc.arg('description'),
    license       = sqlc.narg('license'),
    compatibility = sqlc.narg('compatibility'),
    metadata      = sqlc.arg('metadata'),
    allowed_tools = sqlc.narg('allowed_tools'),
    body          = sqlc.arg('body'),
    source        = sqlc.arg('source'),
    source_ref    = sqlc.narg('source_ref'),
    source_sha    = sqlc.narg('source_sha'),
    updated_at    = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteSkill :exec
DELETE FROM skills WHERE id = $1;
