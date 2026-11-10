-- The notices due now, at most one per conversation: the oldest still pending,
-- so "you're in" never overtakes the "waiting" before it. A claim is a
-- committed lease, as for replies (replies.sql): attempts is the claim's own
-- number, and every mark below matches on it.
-- name: ClaimDueNotices :many
WITH claimed AS (
    UPDATE notices n
    SET attempts = n.attempts + 1,
        next_attempt_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::float8)
    WHERE n.id IN (
        SELECT d.id FROM notices d
        WHERE d.status = 'pending' AND d.next_attempt_at <= now()
          -- A deleted team's channels take no messages; its notices wait
          -- for the purge that deletes them.
          AND EXISTS (SELECT 1 FROM teams t WHERE t.id = d.team_id AND t.deleted_at IS NULL)
          AND NOT EXISTS (
              SELECT 1 FROM notices o
              WHERE o.channel_id = d.channel_id AND o.session_ref = d.session_ref
                AND o.status = 'pending' AND o.id < d.id)
        ORDER BY d.next_attempt_at, d.id
        LIMIT sqlc.arg('batch_size')
        FOR UPDATE SKIP LOCKED)
    RETURNING n.*
)
SELECT c.id, c.team_id, c.channel_id, c.sender_ref, c.sender_name, c.session_ref, c.session_kind,
       c.in_reply_to, c.kind, c.attempts, ch.provider
FROM claimed c
JOIN channels ch ON ch.id = c.channel_id
ORDER BY c.id;

-- name: NoticeSent :execrows
UPDATE notices SET status = 'sent', sent_at = now(), last_error = NULL
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- name: RetryNotice :execrows
UPDATE notices
SET last_error = sqlc.arg('error')::text,
    next_attempt_at = now() + make_interval(secs => sqlc.arg('retry_seconds')::float8)
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- name: GiveUpNotice :execrows
UPDATE notices SET status = 'failed', last_error = sqlc.arg('error')::text
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');
