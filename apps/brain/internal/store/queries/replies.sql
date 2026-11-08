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
