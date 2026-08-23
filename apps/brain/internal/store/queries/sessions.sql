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
-- is whether the caller holds channel:write in the team, and that mapping is
-- data in rbac.json. A role name in this file would put it back in code.
--
-- user_id = viewer is null when user_id is null, and null is not true, so a
-- direct session whose participant was deleted falls out of every non-
-- moderator's list. Invisible is the safe direction for a private message.
-- name: ListSessionsByChannel :many
SELECT * FROM sessions
WHERE channel_id = sqlc.arg('channel_id')
  AND (kind <> 'direct'
   OR sqlc.arg('moderator')::bool
   OR user_id = sqlc.arg('viewer')::uuid)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (last_message_at, id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY last_message_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- ListSessionsByTeam is what a session started from the dashboard appears in,
-- since it has no channel to be listed under. Same visibility rule.
-- name: ListSessionsByTeam :many
SELECT * FROM sessions
WHERE team_id = sqlc.arg('team_id')
  AND (kind <> 'direct'
   OR sqlc.arg('moderator')::bool
   OR user_id = sqlc.arg('viewer')::uuid)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (last_message_at, id) < (sqlc.arg('after_last_message_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY last_message_at DESC, id DESC
LIMIT sqlc.arg('page_size');
