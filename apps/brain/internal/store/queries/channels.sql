-- name: CreateChannel :one
INSERT INTO channels (team_id, provider, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetChannel :one
-- A deleted team's channel answers as an unknown one, so its webhooks are
-- refused before the gateway fetches any attachment.
SELECT c.*
FROM channels c
JOIN teams t ON t.id = c.team_id
WHERE c.id = $1 AND t.deleted_at IS NULL;

-- name: DeleteChannel :exec
DELETE FROM channels WHERE id = $1;

-- Each channel with when it was last used and how many of its sessions were
-- active in the last day, by the session rule: a member who cannot open a
-- direct session is not told when it last moved. last_message_at comes
-- through a LEFT JOIN on the newest session's id, so a quiet channel reads
-- as NULL; sessions_channel_recent_idx serves both reads.
-- name: ListChannelsByTeam :many
SELECT sqlc.embed(c),
    latest.last_message_at,
    (SELECT count(*) FROM sessions s
     WHERE s.channel_id = c.id
       AND s.last_message_at >= now() - interval '24 hours'
       AND (s.kind <> 'direct'
        OR sqlc.arg('moderator')::bool
        OR s.user_id = sqlc.arg('viewer')::uuid)) AS sessions_24h
FROM channels c
LEFT JOIN sessions latest ON latest.id = (
    SELECT s.id FROM sessions s
    WHERE s.channel_id = c.id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid)
    ORDER BY s.last_message_at DESC, s.id DESC
    LIMIT 1
)
WHERE c.team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (c.created_at, c.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg('page_size');
