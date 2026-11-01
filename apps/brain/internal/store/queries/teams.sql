-- name: CreateTeam :one
INSERT INTO teams (name) VALUES ($1) RETURNING *;

-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;

-- name: DeleteTeam :exec
DELETE FROM teams WHERE id = $1;

-- name: ListTeams :many
SELECT * FROM teams
WHERE deleted_at IS NULL AND 
    (NOT sqlc.arg('use_cursor')::bool OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: PurgeTeams :many
DELETE FROM teams
WHERE id IN (SELECT id FROM teams
             WHERE deleted_at < sqlc.arg('before')::timestamptz
             ORDER BY deleted_at LIMIT sqlc.arg('limit'))
RETURNING id;

-- name: GetLiveTeam :one
SELECT * FROM teams WHERE id = $1 AND deleted_at IS NULL;

-- Refused while a run is queued or running. The UPDATE takes the row lock,
-- which conflicts with LockLiveTeam's FOR SHARE, so a run is either seen by
-- the NOT EXISTS or sees deleted_at set. Deleting a deleted team matches
-- nothing, so the first deleted_at, and with it the purge date, stays.
-- name: SoftDeleteTeam :execrows
UPDATE teams SET deleted_at = now()
WHERE teams.id = $1
  AND teams.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM runs
                  WHERE runs.team_id = teams.id
                    AND runs.status IN ('queued', 'running'));

-- A live team that now holds the same name makes this fail with 23505.
-- name: RestoreTeam :one
UPDATE teams SET deleted_at = NULL WHERE id = $1 RETURNING *;

-- name: ListDeletedTeams :many
SELECT * FROM teams
WHERE deleted_at IS NOT NULL AND
    (NOT sqlc.arg('use_cursor')::bool OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- Taken first by anything that starts work in a team, in the same
-- transaction as the write.
-- name: LockLiveTeam :one
SELECT id FROM teams WHERE id = $1 AND deleted_at IS NULL FOR SHARE;
