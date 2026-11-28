-- A run's workspace call refused for want of room: its wait, opened by the
-- first refusal, only while the run is running (a run cancelled or parked
-- meanwhile opens none). A later refusal before the run is told how the wait
-- ended is the same wait, keeping when it began; one the room ended opens
-- again (the room it was made went before the run could use it). One a
-- person, a cancel or the hour ended is not reopened: no row.
-- name: OpenRoomWait :one
INSERT INTO room_waits (team_id, run_id, root_run_id, session_id, reason, retry_at)
SELECT r.team_id, r.id, r.root_run_id, r.session_id, sqlc.arg(reason),
       now() + make_interval(secs => sqlc.arg(first_seconds)::float8)
FROM runs r
WHERE r.id = sqlc.arg(run_id) AND r.status = 'running'
ON CONFLICT (run_id) WHERE told_at IS NULL DO UPDATE SET
    reason   = EXCLUDED.reason,
    retry_at = CASE WHEN room_waits.ended IS NULL THEN room_waits.retry_at ELSE EXCLUDED.retry_at END,
    ended    = NULL,
    ended_at = NULL
WHERE room_waits.ended IS NULL OR room_waits.ended = 'room'
RETURNING *;

-- The waits whose time to try again has come, read without a lock: each is
-- then ended or pushed back under its root's session. gave_up is a wait that
-- began at least give_up_seconds ago. A wait due within 5 seconds counts as
-- due: its retry_at is set after the room was tried, a moment past the sweep
-- that tried it, so it would otherwise miss the sweep its gap ends on.
-- name: ListDueRoomWaits :many
SELECT id, team_id, session_id, root_run_id,
       (since <= now() - make_interval(secs => sqlc.arg(give_up_seconds)::float8))::bool AS gave_up
FROM room_waits
WHERE ended IS NULL AND retry_at <= now() + interval '5 seconds'
ORDER BY retry_at
LIMIT sqlc.arg('batch');

-- Tried again and still no room: the next try after as long as it has
-- waited, at least min_seconds and at most max_seconds, so a short wait is
-- tried often and a long one is not.
-- name: PushRoomWait :execrows
UPDATE room_waits SET retry_at = now() + make_interval(secs => least(
    sqlc.arg(max_seconds)::float8,
    greatest(sqlc.arg(min_seconds)::float8, extract(epoch FROM now() - since))))
WHERE id = sqlc.arg(id) AND ended IS NULL;

-- name: EndRoomWait :execrows
UPDATE room_waits SET ended = sqlc.arg(ended)::text, ended_at = now()
WHERE id = sqlc.arg(id) AND ended IS NULL;

-- Every wait still open in a run tree, ended at once: a person who wrote
-- again, or a tree that ended.
-- name: EndTreeRoomWaits :execrows
UPDATE room_waits SET ended = sqlc.arg(ended)::text, ended_at = now()
WHERE root_run_id = sqlc.arg(root_run_id) AND ended IS NULL;

-- What a resumed run is told: its waits that ended since it parked.
-- name: ListUntoldRoomWaits :many
SELECT * FROM room_waits
WHERE run_id = $1 AND ended IS NOT NULL AND told_at IS NULL
ORDER BY since, id;

-- name: MarkRoomWaitsTold :exec
UPDATE room_waits SET told_at = now()
WHERE id = ANY(sqlc.arg('ids')::uuid[]) AND ended IS NOT NULL AND told_at IS NULL;
