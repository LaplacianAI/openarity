-- Where a schedule or a watcher may answer, besides its own conversation.
-- See internal/triggers.

-- Conversations a person talked in lately, on a chat rather than the
-- dashboard, newest first: a schedule may answer there. Never another
-- schedule's or watcher's own.
-- name: ListTargetChats :many
SELECT ss.id, ss.channel_id::uuid AS channel_id, ch.name AS channel_name, ch.provider,
       ss.kind, ss.provider_ref::text AS provider_ref, ss.user_id,
       max(m.received_at)::timestamptz AS last_said,
       (array_agg(left(m.text, 200) ORDER BY m.received_at DESC))[1]::text AS last_text
FROM sessions ss
JOIN channels ch ON ch.id = ss.channel_id
JOIN messages m ON m.session_id = ss.id
WHERE ss.team_id = sqlc.arg('team_id') AND ss.status = 'open'
  AND ss.schedule_id IS NULL AND ss.watcher_id IS NULL AND ch.provider <> 'dashboard'
  AND m.user_id = sqlc.arg('user_id')::uuid AND NOT m.answer
  AND m.received_at > sqlc.arg('since')::timestamptz
  AND (sqlc.narg('session_id')::uuid IS NULL OR ss.id = sqlc.narg('session_id')::uuid)
GROUP BY ss.id, ch.name, ch.provider
ORDER BY last_said DESC, ss.id
LIMIT sqlc.arg('page_size');

-- Group chats the bot is in, as their conversations show: a schedule may post
-- at their top level. A platform whose groups have no threads (Telegram,
-- WhatsApp) names a group's top level by the group's own conversation.
-- name: ListTargetGroups :many
SELECT ss.id, ss.channel_id::uuid AS channel_id, ch.name AS channel_name, ch.provider,
       ss.provider_ref::text AS provider_ref, ss.last_message_at
FROM sessions ss
JOIN channels ch ON ch.id = ss.channel_id
WHERE ss.team_id = sqlc.arg('team_id') AND ss.status = 'open' AND ss.kind = 'group'
  AND ss.schedule_id IS NULL AND ss.watcher_id IS NULL AND ch.provider <> 'dashboard'
  AND (sqlc.narg('session_id')::uuid IS NULL OR ss.id = sqlc.narg('session_id')::uuid)
ORDER BY ss.last_message_at DESC, ss.id
LIMIT sqlc.arg('page_size');

-- Channels a person is let in on, with the platform's id for them, newest
-- link first: where a schedule may open their direct conversation.
-- name: ListSenderChannels :many
SELECT DISTINCT ON (c.id) c.id, c.name, c.provider, cs.sender_ref
FROM channel_senders cs
JOIN channels c ON c.id = cs.channel_id
WHERE c.team_id = sqlc.arg('team_id') AND cs.user_id = sqlc.arg('user_id')::uuid
  AND (sqlc.narg('channel_id')::uuid IS NULL OR c.id = sqlc.narg('channel_id')::uuid)
ORDER BY c.id, cs.created_at DESC;

-- A team's channels on the named providers: those a channel id can be looked
-- up on.
-- name: ListChannelsOn :many
SELECT id, name, provider FROM channels
WHERE team_id = sqlc.arg('team_id') AND provider = ANY(sqlc.arg('providers')::text[])
  AND (sqlc.narg('channel_id')::uuid IS NULL OR id = sqlc.narg('channel_id')::uuid)
ORDER BY name, id;
