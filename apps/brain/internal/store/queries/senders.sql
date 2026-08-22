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

-- ApproveSender links a sender to a user and takes them out of the queue.
--
-- One statement rather than two inside a transaction. Postgres runs a
-- statement atomically, and a data-modifying CTE executes to completion
-- whether or not the primary query reads its output — so the link and the
-- dequeue cannot come apart, and it is one round trip instead of four.
--
-- They have to be atomic because nothing here retries. The gateway's writes
-- can be merely idempotent, since the provider sends again; an admin approves
-- once. A link left with its pending row in place shows the next admin work
-- that is already done, and they approve the same person twice.
-- name: ApproveSender :exec
WITH linked AS (
    INSERT INTO channel_senders (channel_id, sender_ref, user_id)
    VALUES (sqlc.arg('channel_id'), sqlc.arg('sender_ref'), sqlc.arg('user_id'))
    ON CONFLICT (channel_id, sender_ref) DO UPDATE SET user_id = EXCLUDED.user_id
)
DELETE FROM pending_senders
WHERE pending_senders.channel_id = sqlc.arg('channel_id')
  AND pending_senders.sender_ref = sqlc.arg('sender_ref');

-- RemoveSender is both "this person should not speak here any more" and "this
-- pending row is spam". One statement covers both, because the caller cannot
-- always tell which they are looking at and the wrong guess leaves a row
-- behind.
--
-- It is not a block. Their next message queues them again, which is what makes
-- a mistaken removal recoverable — and a removal of somebody persistent
-- ineffective on its own.
-- name: RemoveSender :exec
WITH unlinked AS (
    DELETE FROM channel_senders
    WHERE channel_senders.channel_id = sqlc.arg('channel_id')
      AND channel_senders.sender_ref = sqlc.arg('sender_ref')
)
DELETE FROM pending_senders
WHERE pending_senders.channel_id = sqlc.arg('channel_id')
  AND pending_senders.sender_ref = sqlc.arg('sender_ref');
