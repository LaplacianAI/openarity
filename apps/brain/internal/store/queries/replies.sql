-- The reply to a channel run, written in the transaction that finishes it. It
-- answers the newest message of the conversation, which is the last one the run
-- read whether it came in as the input or as a steer. A run that does not
-- answer a channel, or whose session came from none, inserts nothing and the
-- caller gets pgx.ErrNoRows.
-- name: InsertReply :one
INSERT INTO replies (team_id, run_id, session_id, channel_id, in_reply_to, kind, text)
SELECT r.team_id, r.id, r.session_id, s.channel_id,
       (SELECT m.id FROM messages m WHERE m.session_id = r.session_id
        ORDER BY m.received_at DESC, m.id DESC LIMIT 1),
       sqlc.arg('kind')::text, sqlc.arg('text')::text
FROM runs r
JOIN sessions s ON s.id = r.session_id
WHERE r.id = sqlc.arg('run_id')::uuid AND r.reply_to_channel AND s.channel_id IS NOT NULL
RETURNING *;

-- What was said in a conversation before a moment, newest first: the messages
-- and the answers sent back. An apology is left out; it says nothing the model
-- needs. Who wrote a message is the bound user's email, the only name stored.
-- name: ListSessionTurns :many
SELECT kind, text, at, author FROM (
    SELECT 'message'::text AS kind, m.text, m.received_at AS at, u.email AS author
    FROM messages m
    LEFT JOIN users u ON u.id = m.user_id
    WHERE m.session_id = sqlc.arg('session_id')::uuid
    UNION ALL
    SELECT 'reply'::text, rp.text, rp.created_at, NULL::text
    FROM replies rp
    WHERE rp.session_id = sqlc.arg('session_id')::uuid AND rp.kind = 'answer'
) turns
WHERE at < sqlc.arg('before')::timestamptz
ORDER BY at DESC
LIMIT sqlc.arg('turns')::int;

-- The replies due now, at most one per conversation: the oldest still pending.
-- A conversation is read in order, so a reply waiting to be retried holds back
-- the ones after it, and only those.
--
-- A claim is a committed mark, not a held lock: the sender posts to the
-- provider between claiming and marking, and a transaction held open across
-- that would hold a connection for as long as the provider is slow. The claim
-- pushes next_attempt_at out by the lease, so nobody else takes the reply while
-- it is being sent, and a sender that died with it is retried once the lease
-- runs out. SKIP LOCKED only keeps two senders from claiming one row at once.
--
-- attempts is the claim's own number. The marks below match on it, so a
-- sender whose lease ran out cannot mark a newer attempt.
-- name: ClaimDueReplies :many
WITH claimed AS (
    UPDATE replies r
    SET attempts = r.attempts + 1,
        next_attempt_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::float8)
    WHERE r.id IN (
        SELECT d.id FROM replies d
        WHERE d.status = 'pending' AND d.next_attempt_at <= now()
          -- A deleted team's channels take no messages; its replies wait for
          -- the purge that deletes them rather than going out after it.
          AND EXISTS (SELECT 1 FROM teams t WHERE t.id = d.team_id AND t.deleted_at IS NULL)
          AND NOT EXISTS (
              SELECT 1 FROM replies o
              WHERE o.session_id = d.session_id AND o.status = 'pending' AND o.id < d.id)
        ORDER BY d.next_attempt_at, d.id
        LIMIT sqlc.arg('batch_size')
        FOR UPDATE SKIP LOCKED)
    RETURNING r.*
)
SELECT c.id, c.run_id, c.team_id, c.channel_id, c.kind, c.text, c.attempts,
       ch.provider, ch.reply_url,
       s.provider_ref AS session_ref, s.kind AS session_kind,
       m.external_id AS in_reply_to
FROM claimed c
JOIN channels ch ON ch.id = c.channel_id
JOIN sessions s ON s.id = c.session_id
LEFT JOIN messages m ON m.id = c.in_reply_to
ORDER BY c.id;

-- name: ReplySent :execrows
UPDATE replies SET status = 'sent', sent_at = now(), last_error = NULL
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- name: RetryReply :execrows
UPDATE replies
SET last_error = sqlc.arg('error')::text,
    next_attempt_at = now() + make_interval(secs => sqlc.arg('retry_seconds')::float8)
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- name: GiveUpReply :execrows
UPDATE replies SET status = 'failed', last_error = sqlc.arg('error')::text
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- What became of each of these runs' replies. A run that answers no channel
-- has none and is not in the result. Rides replies_one_per_run.
-- name: ListRunReplies :many
SELECT run_id, kind, status, attempts, last_error, sent_at, next_attempt_at
FROM replies
WHERE run_id = ANY(sqlc.arg('run_ids')::uuid[]);
