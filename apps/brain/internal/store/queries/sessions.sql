-- EnsureSession is find-or-create and "somebody spoke" in one statement.
--
-- ON CONFLICT names the partial index's predicate, so Postgres infers that
-- index rather than looking for a full one — which is what makes a closed
-- session invisible to the conflict and the next message open a fresh row.
-- Nothing closes a session today; the shape is here so that when something
-- does, this query does not change.
--
-- kind is deliberately not updated. A conversation does not become a thread
-- because one message arrived differently, and the adapter that said "direct"
-- the first time is the one that saw it start.
-- name: EnsureSession :one
INSERT INTO sessions (team_id, channel_id, provider_ref, kind)
VALUES (
    sqlc.arg('team_id')::uuid,
    sqlc.arg('channel_id')::uuid,
    sqlc.arg('provider_ref')::text,
    sqlc.arg('kind')::text
)
ON CONFLICT (channel_id, provider_ref) WHERE status = 'open'
DO UPDATE SET last_message_at = now()
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- name: ListSessionsByChannel :many
SELECT * FROM sessions
WHERE channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (last_message_at, id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY last_message_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- ListSessionsByTeam is what a session started from the dashboard appears in,
-- since it has no channel to be listed under.
-- name: ListSessionsByTeam :many
SELECT * FROM sessions
WHERE team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (last_message_at, id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY last_message_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: CountSessionsByChannel :one
SELECT count(*) FROM sessions WHERE channel_id = $1;
