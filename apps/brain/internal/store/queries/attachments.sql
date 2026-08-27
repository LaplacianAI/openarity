-- name: CreateAttachment :one
INSERT INTO attachments (
    message_id, session_id, object_key, key_version, media_type, size_bytes, filename
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListAttachmentsByMessage :many
SELECT * FROM attachments
WHERE message_id = $1
ORDER BY created_at, id;

-- The read path's only query. It returns the team in the same round trip that
-- fetches the row, so authorisation never needs a second one — and the team is
-- what selects the key that opens the object.
--
-- Through sessions, not through channels. A session carries its own team and
-- always has one; its channel_id is nullable, because the dashboard and the
-- API start sessions with no channel behind them. Joining through channels
-- would return no row for those, and an attachment that exists would read as
-- one that does not.
--
-- name: GetAttachmentWithTeam :one
SELECT sqlc.embed(a), s.team_id
FROM attachments a
JOIN messages m ON m.id = a.message_id
JOIN sessions s ON s.id = m.session_id
WHERE a.id = $1;

-- Everything a session has accumulated, which is what an agent reads to
-- answer "the file I sent earlier" — a message can name one that arrived
-- twenty messages ago. Straight off attachments.session_id rather than
-- through messages: no index can express that join, so it reads every message
-- in the session and every attachment in the database to return a few.
--
-- name: ListAttachmentsBySession :many
SELECT * FROM attachments
WHERE session_id = $1
ORDER BY created_at, id;

-- Deletion asks this before removing an object: another row pointing at the
-- same key means the bytes are still somebody's. Always 1 today, and not
-- always 1 once identical files share an object.
--
-- name: CountAttachmentsByObjectKey :one
SELECT count(*) FROM attachments WHERE object_key = $1;
