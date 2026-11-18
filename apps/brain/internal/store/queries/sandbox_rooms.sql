-- A room's owner. One call at a time makes the room for an owner, across every
-- brain: the keeper holds this lock from before it reads the row until it has
-- saved the room, so two callers that find no room make one. It is a session
-- lock, held on one pooled connection for as long as a cold start takes, up to
-- the start timeout; pg_advisory_lock waits and a cancelled context ends the
-- wait.
--
-- The key is hashed in SQL, so the sweep of idle rooms below can try the same
-- lock with the same key and leave a room being made or reused alone.
--
-- name: LockSandboxRoom :exec
SELECT pg_advisory_lock(hashtextextended('sandbox-room:' || sqlc.arg('owner_kind')::text || ':' || sqlc.arg('owner_id')::text, 0));

-- name: UnlockSandboxRoom :one
SELECT pg_advisory_unlock(hashtextextended('sandbox-room:' || sqlc.arg('owner_kind')::text || ':' || sqlc.arg('owner_id')::text, 0))::bool AS released;

-- name: GetSandboxRoom :one
SELECT * FROM sandbox_rooms
WHERE owner_kind = sqlc.arg('owner_kind') AND owner_id = sqlc.arg('owner_id');

-- name: CreateSandboxRoom :one
INSERT INTO sandbox_rooms (
    id, team_id, owner_kind, owner_id, provider, provider_url, room_id, spec_hash, sandbox_secret_id
) VALUES (
    sqlc.arg('id'), sqlc.arg('team_id'), sqlc.arg('owner_kind'), sqlc.arg('owner_id'),
    sqlc.arg('provider'), sqlc.narg('provider_url'), sqlc.arg('room_id'), sqlc.arg('spec_hash'),
    sqlc.arg('sandbox_secret_id')
)
RETURNING *;

-- name: TouchSandboxRoom :exec
UPDATE sandbox_rooms SET last_used_at = now() WHERE id = $1;

-- A room replaced, or found gone, or one that does not answer: its row goes,
-- and the trigger records the room and its token, which the sweep deletes.
-- Deleted by id, so a row another brain has already replaced is left alone.
--
-- name: DeleteSandboxRoom :exec
DELETE FROM sandbox_rooms WHERE id = $1;

-- Rooms unused since the cutoff. A room whose owner is being ensured right
-- now is skipped rather than waited for: it is in use. Deleting the rows
-- tombstones them; the same sweep deletes the rooms.
--
-- name: ExpireIdleSandboxRooms :execrows
DELETE FROM sandbox_rooms
WHERE last_used_at < sqlc.arg('cutoff')::timestamptz
  AND pg_try_advisory_xact_lock(hashtextextended('sandbox-room:' || owner_kind || ':' || owner_id::text, 0));

-- name: ClaimDeletedRooms :many
UPDATE deleted_rooms
SET attempts = attempts + 1, last_attempt_at = now()
WHERE id IN (
    SELECT d.id FROM deleted_rooms d
    WHERE d.last_attempt_at IS NULL OR d.last_attempt_at < sqlc.arg('retry_before')
    ORDER BY d.last_attempt_at NULLS FIRST, d.deleted_at
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: GetDeletedRoom :one
SELECT * FROM deleted_rooms WHERE id = $1;

-- name: ForgetDeletedRoom :exec
DELETE FROM deleted_rooms WHERE id = $1;

-- name: DeletedRoomBacklog :many
SELECT count(*) OVER () AS outstanding, deleted_at AS oldest
FROM deleted_rooms
ORDER BY deleted_at
LIMIT 1;
