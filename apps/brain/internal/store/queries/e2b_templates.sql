-- One call at a time asks E2B for a team's template, across every brain: the
-- caller holds this lock from before it reads the row until it has saved what
-- E2B said, so a save and a room's first start that both find no build start
-- one. A session lock, as a room's is; asking E2B takes seconds, not minutes.
--
-- name: LockE2BTemplate :exec
SELECT pg_advisory_lock(hashtextextended('e2b-template:' || sqlc.arg('team_id')::text || ':' || sqlc.arg('sandbox_secret_id')::text || ':' || sqlc.arg('name')::text, 0));

-- name: UnlockE2BTemplate :one
SELECT pg_advisory_unlock(hashtextextended('e2b-template:' || sqlc.arg('team_id')::text || ':' || sqlc.arg('sandbox_secret_id')::text || ':' || sqlc.arg('name')::text, 0))::bool AS released;

-- name: GetE2BTemplate :one
SELECT * FROM e2b_templates
WHERE team_id = sqlc.arg('team_id')
  AND sandbox_secret_id = sqlc.arg('sandbox_secret_id')
  AND name = sqlc.arg('name');

-- A build asked for, or one E2B refused to start: either way the row says
-- what happened last, and a new build's clock starts now.
--
-- name: SaveE2BTemplate :one
INSERT INTO e2b_templates (
    team_id, sandbox_secret_id, name, template_id, build_id, status, error
) VALUES (
    sqlc.arg('team_id'), sqlc.arg('sandbox_secret_id'), sqlc.arg('name'),
    sqlc.narg('template_id'), sqlc.narg('build_id'), sqlc.arg('status'), sqlc.narg('error')
)
ON CONFLICT (team_id, sandbox_secret_id, name) DO UPDATE SET
    template_id = EXCLUDED.template_id,
    build_id    = EXCLUDED.build_id,
    status      = EXCLUDED.status,
    error       = EXCLUDED.error,
    started_at  = now(),
    updated_at  = now()
RETURNING *;

-- The builds still running, oldest checked first, for the job that reads
-- their status back.
--
-- name: ListBuildingE2BTemplates :many
SELECT * FROM e2b_templates
WHERE status = 'building'
ORDER BY updated_at
LIMIT sqlc.arg('max');

-- A build read back as done, or as failed. Only the build that was asked for
-- is finished: one replaced by a newer build since it was listed is left.
--
-- name: FinishE2BTemplate :execrows
UPDATE e2b_templates
SET status = sqlc.arg('status'), error = sqlc.narg('error'), updated_at = now()
WHERE id = sqlc.arg('id') AND build_id = sqlc.arg('build_id') AND status = 'building';

-- A build still running, read back and seen to be, so the next poll reads
-- the others first.
--
-- name: TouchE2BTemplate :exec
UPDATE e2b_templates SET updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'building';

-- The newest template already built under the key, of any name: what rooms
-- start from while a newer release's is still being built.
--
-- name: GetReadyE2BTemplate :one
SELECT * FROM e2b_templates
WHERE team_id = sqlc.arg('team_id')
  AND sandbox_secret_id = sqlc.arg('sandbox_secret_id')
  AND status = 'ready'
ORDER BY updated_at DESC
LIMIT 1;
