-- InsertMessage returns the id it wrote, and no row at all for a replay. Both
-- answers are a 200; the difference is whether anything downstream should run.
-- pgx reports the second as ErrNoRows, which carries the same information the
-- row count used to and also names the message, which attachments need.
--
-- DO NOTHING rather than a DO UPDATE that always returns. A no-op
-- `SET external_id = EXCLUDED.external_id` writes a new row version anyway:
-- measured on 18.6, 200 replays of a single row took the heap from 8 kB to
-- 16 kB. Replays are the hot case here, not the rare one — a provider that
-- does not get its 200 retries — and on a replay there is nothing to attach,
-- because the files were stored the first time.
--
-- The uniqueness is still per channel rather than per session: a provider's
-- message id is unique within its channel, and a retry that arrived after a
-- session closed would otherwise be stored twice under two sessions.
-- name: InsertMessage :one
INSERT INTO messages (channel_id, session_id, user_id, external_id, text, sent_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (channel_id, external_id) DO NOTHING
RETURNING id;

-- name: ListMessagesBySession :many
SELECT * FROM messages
WHERE session_id = sqlc.arg('session_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (received_at, id) < (sqlc.arg('after_received_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY received_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: CountMessagesBySession :one
SELECT count(*) FROM messages WHERE session_id = $1;
