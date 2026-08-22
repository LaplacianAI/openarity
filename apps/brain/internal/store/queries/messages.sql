-- InsertMessage reports the rows written, so a caller can tell a new message
-- from a replay without asking first. Both answers are a 200; the difference
-- is only whether anything downstream should run.
--
-- The uniqueness is still per channel rather than per session: a provider's
-- message id is unique within its channel, and a retry that arrived after a
-- session closed would otherwise be stored twice under two sessions.
-- name: InsertMessage :execrows
INSERT INTO messages (channel_id, session_id, user_id, external_id, text, sent_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (channel_id, external_id) DO NOTHING;

-- name: ListMessagesBySession :many
SELECT * FROM messages
WHERE session_id = sqlc.arg('session_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (received_at, id) < (sqlc.arg('after_received_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY received_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: CountMessagesBySession :one
SELECT count(*) FROM messages WHERE session_id = $1;
