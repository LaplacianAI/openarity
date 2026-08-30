-- name: CreateAttachment :one
INSERT INTO attachments (
    message_id, session_id, team_id,
    object_key, key_version, media_type, size_bytes, filename
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListAttachmentsByMessage :many
SELECT * FROM attachments
WHERE message_id = $1
ORDER BY created_at, id;

-- Everything a session has accumulated, which is what an agent reads to
-- answer "the file I sent earlier" — a message can name one that arrived
-- twenty messages ago. Straight off attachments.session_id rather than
-- through messages: no index can express that join, so it reads every message
-- in the session and every attachment in the database to return a few.
--
-- Ordered newest first and paged on (created_at, id), matching messages and
-- sessions: a page boundary has to be a total order, and created_at repeats
-- when several files arrive on one delivery.
--
-- name: ListAttachmentsBySession :many
SELECT * FROM attachments
WHERE session_id = sqlc.arg('session_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- The bytes route's scope check, done in SQL rather than in Go. The handler
-- has already resolved the session and proven the caller may read it; naming
-- the session here means an attachment from another conversation is a missing
-- row rather than a comparison somebody has to remember to write.
--
-- name: GetAttachmentInSession :one
SELECT * FROM attachments
WHERE id = $1 AND session_id = $2;

-- The sweeper asks this before removing an object: another row pointing at the
-- same key means the bytes are still somebody's, and the tombstone is dropped
-- rather than acted on. Always 1 today, and not always 1 once identical files
-- share one.
--
-- name: CountAttachmentsByObjectKey :one
SELECT count(*) FROM attachments WHERE object_key = $1;
