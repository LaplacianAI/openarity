-- InsertMessage reports the rows written, so a caller can tell a new message
-- from a replay without asking first. Both answers are a 200; the difference
-- is only whether anything downstream should run.
-- name: InsertMessage :execrows
INSERT INTO messages (channel_id, user_id, external_id, conversation_ref, text, sent_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (channel_id, external_id) DO NOTHING;

-- name: ListMessagesByChannel :many
SELECT * FROM messages
WHERE channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (received_at, id) < (sqlc.arg('after_received_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY received_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: CountMessagesByChannel :one
SELECT count(*) FROM messages WHERE channel_id = $1;
