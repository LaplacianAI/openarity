-- name: FindChannelSender :one
SELECT user_id FROM channel_senders
WHERE channel_id = $1 AND sender_ref = $2;

-- name: LinkChannelSender :exec
INSERT INTO channel_senders (channel_id, sender_ref, user_id)
VALUES ($1, $2, $3)
ON CONFLICT (channel_id, sender_ref) DO UPDATE SET user_id = EXCLUDED.user_id;

-- name: UnlinkChannelSender :exec
DELETE FROM channel_senders WHERE channel_id = $1 AND sender_ref = $2;

-- name: ListChannelSenders :many
SELECT * FROM channel_senders
WHERE channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, sender_ref) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_ref')::text))
ORDER BY created_at DESC, sender_ref DESC
LIMIT sqlc.arg('page_size');

-- RecordPendingSender is one statement rather than a count followed by an
-- insert, because the count is a decision an unauthenticated caller races.
-- An existing sender is always refreshed; only a *new* one is subject to the
-- cap, so a busy channel at its limit still shows accurate last_seen values.
-- It reports the rows written, so the caller can tell "recorded" from
-- "dropped at the cap" without a second query.
-- name: RecordPendingSender :execrows
INSERT INTO pending_senders (channel_id, sender_ref, sender_name)
SELECT sqlc.arg('channel_id')::uuid, sqlc.arg('sender_ref')::text, sqlc.arg('sender_name')::text
WHERE EXISTS (
        SELECT 1 FROM pending_senders
        WHERE channel_id = sqlc.arg('channel_id')::uuid
          AND sender_ref = sqlc.arg('sender_ref')::text)
   OR (SELECT count(*) FROM pending_senders
        WHERE channel_id = sqlc.arg('channel_id')::uuid) < sqlc.arg('cap')::bigint
ON CONFLICT (channel_id, sender_ref) DO UPDATE
SET last_seen  = now(),
    seen_count = pending_senders.seen_count + 1,
    -- Somebody who renames themselves should show under the name they are
    -- using now, or an admin approves a row that no longer matches anything
    -- they can see in the provider.
    sender_name = EXCLUDED.sender_name;

-- name: CountPendingSenders :one
SELECT count(*) FROM pending_senders WHERE channel_id = $1;

-- name: ListPendingSenders :many
SELECT * FROM pending_senders
WHERE channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (first_seen, sender_ref) < (sqlc.arg('after_first_seen')::timestamptz, sqlc.arg('after_ref')::text))
ORDER BY first_seen DESC, sender_ref DESC
LIMIT sqlc.arg('page_size');

-- name: DeletePendingSender :exec
DELETE FROM pending_senders WHERE channel_id = $1 AND sender_ref = $2;
