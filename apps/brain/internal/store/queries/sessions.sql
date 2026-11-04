-- A run started through the API with no session gets one of its own: direct,
-- with no channel, owned by whoever started it. So by the rule above only they
-- and a moderator can read it, which is the right default for a conversation
-- nobody else was invited to.
-- name: CreateDirectSession :one
INSERT INTO sessions (team_id, kind, user_id)
VALUES (sqlc.arg('team_id'), 'direct', sqlc.arg('user_id')::uuid)
RETURNING *;

-- One session, under the same rule as ListSessionsByTeam. Starting a run in a
-- session uses this, so a run cannot be put into a conversation its caller
-- could not read.
-- name: GetSessionVisible :one
SELECT * FROM sessions
WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id')
  AND (kind <> 'direct'
   OR sqlc.arg('moderator')::bool
   OR user_id = sqlc.arg('viewer')::uuid);
-- EnsureSession is find-or-create and "somebody spoke" in one statement.
--
-- ON CONFLICT names the partial index's predicate, so Postgres infers that
-- index rather than looking for a full one — which is what makes a closed
-- session invisible to the conflict and the next message open a fresh row.
-- Nothing closes a session today; the shape is here so that when something
-- does, this query does not change.
--
-- kind is deliberately not updated. A conversation does not become a thread
-- because one message arrived differently, and the adapter that said "direct"
-- the first time is the one that saw it start.
--
-- Nor is user_id. For a direct session it is who the conversation is with,
-- and that is decided when it starts; letting a later message move it would
-- let a second approved sender take over somebody else's private thread.
-- name: EnsureSession :one
INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id)
VALUES (
    sqlc.arg('team_id')::uuid,
    sqlc.arg('channel_id')::uuid,
    sqlc.arg('provider_ref')::text,
    sqlc.arg('kind')::text,
    sqlc.narg('user_id')::uuid
)
ON CONFLICT (channel_id, provider_ref) WHERE status = 'open'
DO UPDATE SET last_message_at = now()
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- A direct session is one person's conversation, and every member of the team
-- could read it until this filter existed. The rule:
--
--   group and thread   the whole team, as before — a shared room is shared
--   direct             the person it is with, plus a moderator
--
-- `moderator` is passed in rather than decided here, because who may moderate
-- is whether the caller holds session:read_all in the team, and that mapping
-- is data in rbac.json. A role name in this file would put it back in code.
--
-- user_id = viewer is null when user_id is null, and null is not true, so a
-- direct session whose participant was deleted falls out of every non-
-- moderator's list. Invisible is the safe direction for a private message.
-- name: ListSessionsByChannel :many
SELECT sqlc.embed(s),
       c.name     AS channel_name,
       pu.email   AS participant_email,
       pu.subject AS participant_subject,
       m.text     AS last_message,
       lr.status  AS latest_run_status
FROM sessions s
LEFT JOIN channels c ON c.id = s.channel_id
LEFT JOIN messages m ON m.id = (
    SELECT id FROM messages
    WHERE session_id = s.id
    ORDER BY received_at DESC, id DESC
    LIMIT 1)
LEFT JOIN runs lr ON lr.id = (
    SELECT id FROM runs
    WHERE session_id = s.id AND id = root_run_id
    ORDER BY created_at DESC, id DESC
    LIMIT 1)
LEFT JOIN users pu ON pu.id = CASE WHEN s.kind = 'direct' THEN s.user_id ELSE m.user_id END
WHERE s.channel_id = sqlc.arg('channel_id')
  AND (s.kind <> 'direct'
   OR sqlc.arg('moderator')::bool
   OR s.user_id = sqlc.arg('viewer')::uuid)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (s.last_message_at, s.id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY s.last_message_at DESC, s.id DESC
LIMIT sqlc.arg('page_size');

-- ListSessionsByTeam is what a session started from the dashboard appears in,
-- since it has no channel to be listed under. Same visibility rule.
--
-- Each row carries its channel's name, who it is with, the newest message
-- and the newest root run's status. The newest message and run are picked by
-- id in a subquery and joined as plain LEFT JOINs, not LATERAL: sqlc reads
-- a lateral's columns as never null, and a session with no messages would
-- then fail to scan. The run_statuses filter reads the newest run, so it
-- asks "is the latest run failed", not "did any run fail", and leaves out a
-- session with no run whenever it is set.
-- name: ListSessionsByTeam :many
SELECT sqlc.embed(s),
       c.name     AS channel_name,
       pu.email   AS participant_email,
       pu.subject AS participant_subject,
       m.text     AS last_message,
       lr.status  AS latest_run_status
FROM sessions s
LEFT JOIN channels c ON c.id = s.channel_id
LEFT JOIN messages m ON m.id = (
    SELECT id FROM messages
    WHERE session_id = s.id
    ORDER BY received_at DESC, id DESC
    LIMIT 1)
LEFT JOIN runs lr ON lr.id = (
    SELECT id FROM runs
    WHERE session_id = s.id AND id = root_run_id
    ORDER BY created_at DESC, id DESC
    LIMIT 1)
LEFT JOIN users pu ON pu.id = CASE WHEN s.kind = 'direct' THEN s.user_id ELSE m.user_id END
WHERE s.team_id = sqlc.arg('team_id')
  AND (s.kind <> 'direct'
   OR sqlc.arg('moderator')::bool
   OR s.user_id = sqlc.arg('viewer')::uuid)
  AND (sqlc.narg('channel_id')::uuid IS NULL OR s.channel_id = sqlc.narg('channel_id')::uuid)
  AND (COALESCE(cardinality(sqlc.arg('statuses')::text[]), 0) = 0
   OR s.status = ANY(sqlc.arg('statuses')::text[]))
  AND (COALESCE(cardinality(sqlc.arg('run_statuses')::text[]), 0) = 0
   OR lr.status = ANY(sqlc.arg('run_statuses')::text[]))
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (s.last_message_at, s.id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY s.last_message_at DESC, s.id DESC
LIMIT sqlc.arg('page_size');
