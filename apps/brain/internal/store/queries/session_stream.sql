-- A session's stream: its messages and the events of every run in it, after
-- a sequence number, in order. A message's user is who said it; a run event
-- carries its run's agent and parent, and a finished one the run's output.
-- name: ListSessionEvents :many
SELECT x.seq, x.at, x.kind, x.run_id, x.payload, x.agent, x.parent_run_id, x.output
FROM (
    SELECT m.session_seq AS seq, m.received_at AS at, 'message'::text AS kind,
           NULL::uuid AS run_id,
           jsonb_build_object('id', m.id, 'user_id', m.user_id, 'text', m.text, 'asks', m.asks,
               -- Written in the message's own transaction, so a reader never
               -- sees the message without its files.
               'files', (SELECT coalesce(jsonb_agg(jsonb_build_object(
                               'id', a.id, 'filename', a.filename,
                               'media_type', a.media_type, 'size', a.size_bytes)
                             ORDER BY a.created_at, a.id), '[]'::jsonb)
                         FROM attachments a WHERE a.message_id = m.id)) AS payload,
           NULL::text AS agent, NULL::uuid AS parent_run_id, NULL::text AS output
    FROM messages m
    WHERE m.session_id = sqlc.arg('session_id')::uuid AND m.session_seq > sqlc.arg('after')::bigint
    UNION ALL
    SELECT e.session_seq, e.created_at, e.kind, e.run_id, e.payload,
           r.agent_name, r.parent_run_id, CASE WHEN e.kind = 'finished' THEN r.output END
    FROM run_events e
    JOIN runs r ON r.id = e.run_id
    WHERE e.session_id = sqlc.arg('session_id')::uuid AND e.session_seq > sqlc.arg('after')::bigint
) x
ORDER BY x.seq
LIMIT sqlc.arg('page_size');

-- The session a run belongs to, locked: what every write of a run's events,
-- and every ending of a run, takes before anything else.
-- name: LockRunSession :one
SELECT s.id FROM sessions s
JOIN runs r ON r.session_id = s.id
WHERE r.id = $1
FOR UPDATE OF s;

-- A finished event for each of these runs that has ended without one: the run
-- cancelled before a worker took it, the children cancelled or reaped with
-- their parent. One already written is left alone.
-- name: AppendFinishedEvents :exec
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT r.id,
       coalesce((SELECT max(e.seq) FROM run_events e WHERE e.run_id = r.id), 0) + 1,
       'finished',
       jsonb_strip_nulls(jsonb_build_object('status', r.status, 'error', r.error))
FROM runs r
WHERE r.id = ANY(sqlc.arg('ids')::uuid[])
  AND r.status IN ('succeeded', 'failed', 'cancelled')
  AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.run_id = r.id AND e.kind = 'finished')
ORDER BY r.depth, r.id;

-- A message that asked for an answer: it started a run, or was steered into
-- one. Until a run takes it, it is pending.
-- name: MarkMessageAsks :exec
UPDATE messages SET asks = true WHERE id = $1;

-- A run taking pending messages of its session, oldest first, and saying so
-- in its events: the one given, the oldest one when first_only, or all of
-- them. Taking none writes nothing.
-- name: ConsumeMessages :execrows
WITH taken AS (
    UPDATE messages m SET consumed_by = sqlc.arg('run_id')::uuid
    WHERE m.id IN (
        SELECT p.id FROM messages p
        JOIN runs r ON r.session_id = p.session_id
        WHERE r.id = sqlc.arg('run_id')::uuid
          AND p.asks AND p.consumed_by IS NULL
          AND (sqlc.narg('only')::uuid IS NULL OR p.id = sqlc.narg('only')::uuid)
        ORDER BY p.session_seq
        LIMIT CASE WHEN sqlc.arg('first_only')::bool THEN 1 END)
    RETURNING m.id, m.session_seq
)
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT sqlc.arg('run_id')::uuid,
       coalesce((SELECT max(e.seq) FROM run_events e WHERE e.run_id = sqlc.arg('run_id')::uuid), 0) + 1,
       'consumed',
       jsonb_build_object('message_ids', jsonb_agg(t.id ORDER BY t.session_seq))
FROM taken t
HAVING count(*) > 0;

-- The team's dashboard channel, which every team has one of.
-- name: DashboardChannel :one
SELECT * FROM channels WHERE team_id = $1 AND provider = 'dashboard';

-- A message by the key it was sent with, in a channel.
-- name: GetMessageByExternalID :one
SELECT * FROM messages WHERE channel_id = $1 AND external_id = $2;

-- Sessions in id order, a page at a time, for the backfill.
-- name: ListSessionIDsAfter :many
SELECT id FROM sessions
WHERE id > sqlc.arg('after')::uuid
ORDER BY id
LIMIT sqlc.arg('page_size');

-- Numbers a session's rows from before its stream, in the order they
-- happened, counting down to 0, so they sit before every row numbered since.
-- Only rows with no number are touched: run again, it numbers nothing. Ties
-- in time go by run, then the run's own order, then message id. Hold the
-- session's lock: a row from before the stream can only be one of these, but
-- the numbers must not move under a reader.
-- name: BackfillSessionSeq :one
WITH ordered AS (
    SELECT m.id AS message_id, NULL::uuid AS run_id, NULL::int AS run_seq, m.received_at AS at
    FROM messages m
    WHERE m.session_id = sqlc.arg('session_id')::uuid AND m.session_seq IS NULL
    UNION ALL
    SELECT NULL::uuid, e.run_id, e.seq, e.created_at
    FROM run_events e
    JOIN runs r ON r.id = e.run_id
    WHERE r.session_id = sqlc.arg('session_id')::uuid AND e.session_seq IS NULL
), numbered AS (
    SELECT o.*,
           row_number() OVER (ORDER BY o.at, o.run_id NULLS FIRST, o.run_seq, o.message_id)
               - count(*) OVER () AS n
    FROM ordered o
), lowest AS (
    -- Below what an earlier backfill numbered, should one have: none of
    -- these can have existed then, but the numbers stay unique either way.
    SELECT least(
        coalesce((SELECT min(session_seq) FROM messages WHERE session_id = sqlc.arg('session_id')::uuid), 1),
        coalesce((SELECT min(session_seq) FROM run_events WHERE session_id = sqlc.arg('session_id')::uuid), 1),
        1) - 1 AS top
), messages_done AS (
    UPDATE messages m SET session_seq = nb.n + l.top
    FROM numbered nb, lowest l
    WHERE nb.message_id IS NOT NULL AND m.id = nb.message_id
    RETURNING m.id
), events_done AS (
    UPDATE run_events e SET session_seq = nb.n + l.top, session_id = sqlc.arg('session_id')::uuid
    FROM numbered nb, lowest l
    WHERE nb.run_id IS NOT NULL AND e.run_id = nb.run_id AND e.seq = nb.run_seq
    RETURNING e.run_id
)
SELECT (SELECT count(*) FROM messages_done)::bigint AS messages,
       (SELECT count(*) FROM events_done)::bigint AS events;
