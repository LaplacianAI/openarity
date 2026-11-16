-- name: FindChannelSender :one
SELECT user_id FROM channel_senders
WHERE channel_id = $1 AND sender_ref = $2;

-- name: LinkChannelSender :exec
INSERT INTO channel_senders (channel_id, sender_ref, user_id)
VALUES ($1, $2, $3)
ON CONFLICT (channel_id, sender_ref) DO UPDATE SET user_id = EXCLUDED.user_id;

-- name: UnlinkChannelSender :exec
DELETE FROM channel_senders WHERE channel_id = $1 AND sender_ref = $2;

-- ListChannelSenders is who may speak on a channel, and as whom: each
-- sender's user, that user's role in the channel's team (null once they have
-- left it), and whether they are a channel user of their own.
-- name: ListChannelSenders :many
SELECT sqlc.embed(cs), u.name AS user_name, u.email AS user_email,
       (u.issuer = 'openarity:channel')::bool AS channel_user, tm.role AS role
FROM channel_senders cs
JOIN channels c ON c.id = cs.channel_id
JOIN users u ON u.id = cs.user_id
LEFT JOIN team_members tm ON tm.team_id = c.team_id AND tm.user_id = cs.user_id
WHERE cs.channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (cs.created_at, cs.sender_ref) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_ref')::text))
ORDER BY cs.created_at DESC, cs.sender_ref DESC
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

-- NoteWaitingSender tells a waiting sender once per wait: it marks the pending
-- row and writes the notice in one statement, so two messages arriving at once
-- make one notice. The UPDATE's WHERE is re-checked against the row the first
-- statement committed, and the second finds notified_at set. Where to answer
-- is the provider's ids only. The caller decides the notice is wanted: the
-- message asked the bot, the channel allows notices, its provider sends them.
-- Returns the notice's id, or no rows when the sender was already told.
-- name: NoteWaitingSender :one
WITH due AS (
    UPDATE pending_senders
    SET notified_at         = now(),
        notice_session_ref  = sqlc.arg('session_ref')::text,
        notice_session_kind = sqlc.arg('session_kind')::text,
        notice_message_ref  = sqlc.arg('message_ref')::text
    WHERE channel_id = sqlc.arg('channel_id')::uuid
      AND sender_ref = sqlc.arg('sender_ref')::text
      AND notified_at IS NULL
    RETURNING channel_id, sender_ref, sender_name
)
INSERT INTO notices (team_id, channel_id, sender_ref, sender_name, session_ref, session_kind, in_reply_to, kind)
SELECT sqlc.arg('team_id')::uuid, d.channel_id, d.sender_ref, d.sender_name,
       sqlc.arg('session_ref')::text, sqlc.arg('session_kind')::text, sqlc.arg('message_ref')::text, 'waiting'
FROM due d
RETURNING id;

-- name: CountPendingSenders :one
SELECT count(*) FROM pending_senders WHERE channel_id = $1;

-- name: ListPendingSenders :many
SELECT * FROM pending_senders
WHERE channel_id = sqlc.arg('channel_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (first_seen, sender_ref) < (sqlc.arg('after_first_seen')::timestamptz, sqlc.arg('after_ref')::text))
ORDER BY first_seen DESC, sender_ref DESC
LIMIT sqlc.arg('page_size');

-- ListTeamPendingSenders is every channel's queue as one, oldest first: the
-- sender who has waited longest is the one an approver should see. The
-- cursor carries all three sort keys, because sender_ref is unique only
-- within a channel and two senders can be first seen in the same instant.
-- Each channel holds at most 50, so the sort needs no index of its own.
-- name: ListTeamPendingSenders :many
SELECT sqlc.embed(p), c.name AS channel_name, c.provider AS channel_provider
FROM pending_senders p
JOIN channels c ON c.id = p.channel_id
WHERE c.team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (p.first_seen, p.channel_id, p.sender_ref)
      > (sqlc.arg('after_first_seen')::timestamptz, sqlc.arg('after_channel_id')::uuid, sqlc.arg('after_ref')::text))
ORDER BY p.first_seen, p.channel_id, p.sender_ref
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
--
-- The binding is written only while it is still held by expected_holder, or
-- still unbound when that is null: the handler checked the caller may take
-- the ref from whoever held it, and a binding that changed since was never
-- checked. ON CONFLICT re-reads the row it locked, so a holder changed by a
-- transaction that committed first is seen, and the WHERE refuses the update.
-- The dequeue runs only when the link did, so a refusal changes nothing.
-- Returns how many bindings were written: 0 is a 409, never a success; and
-- the "you're in" notice it wrote, if any, for the caller to kick.
-- name: ApproveSender :one
WITH linked AS (
    INSERT INTO channel_senders (channel_id, sender_ref, user_id)
    VALUES (sqlc.arg('channel_id'), sqlc.arg('sender_ref'), sqlc.arg('user_id'))
    ON CONFLICT (channel_id, sender_ref) DO UPDATE SET user_id = EXCLUDED.user_id
    WHERE channel_senders.user_id IS NOT DISTINCT FROM sqlc.narg('expected_holder')::uuid
    RETURNING 1
), admitted AS (
    -- Every CTE reads the snapshot the statement started with, so this sees
    -- the pending row the dequeue below deletes. "You're in" goes only to a
    -- sender who was told they were waiting, where they were told, and only
    -- while the channel still sends notices.
    INSERT INTO notices (team_id, channel_id, sender_ref, sender_name, session_ref, session_kind, kind)
    SELECT c.team_id, p.channel_id, p.sender_ref, p.sender_name,
           p.notice_session_ref, p.notice_session_kind, 'admitted'
    FROM pending_senders p
    JOIN channels c ON c.id = p.channel_id
    WHERE p.channel_id = sqlc.arg('channel_id')
      AND p.sender_ref = sqlc.arg('sender_ref')
      AND p.notified_at IS NOT NULL
      AND c.notify_strangers
      AND EXISTS (SELECT 1 FROM linked)
    RETURNING id
), dequeued AS (
    DELETE FROM pending_senders
    WHERE pending_senders.channel_id = sqlc.arg('channel_id')
      AND pending_senders.sender_ref = sqlc.arg('sender_ref')
      AND EXISTS (SELECT 1 FROM linked)
)
SELECT (SELECT count(*) FROM linked) AS linked, COALESCE((SELECT id FROM admitted), 0)::bigint AS admitted;

-- RemoveSender is both "this person should not speak here any more" and "this
-- pending row is spam". One statement covers both, because the caller cannot
-- always tell which they are looking at and the wrong guess leaves a row
-- behind.
--
-- It is not a block. Their next message queues them again, which is what makes
-- a mistaken removal recoverable — and a removal of somebody persistent
-- ineffective on its own.
--
-- The binding is deleted only while it is still held by expected_holder, the
-- holder the handler checked. Null means it was unbound when read: then there
-- is no binding to delete, and one written since was never checked, so it
-- stays. A binding moved since is left, and so is the pending row, so a
-- refusal changes nothing. Returns false only for that refusal.
-- name: RemoveSender :one
WITH unlinked AS (
    DELETE FROM channel_senders
    WHERE channel_senders.channel_id = sqlc.arg('channel_id')
      AND channel_senders.sender_ref = sqlc.arg('sender_ref')
      AND channel_senders.user_id = sqlc.narg('expected_holder')::uuid
    RETURNING 1
), dequeued AS (
    DELETE FROM pending_senders
    WHERE pending_senders.channel_id = sqlc.arg('channel_id')
      AND pending_senders.sender_ref = sqlc.arg('sender_ref')
      AND (sqlc.narg('expected_holder')::uuid IS NULL OR EXISTS (SELECT 1 FROM unlinked))
)
SELECT COALESCE(sqlc.narg('expected_holder')::uuid IS NULL OR EXISTS (SELECT 1 FROM unlinked), false)::bool AS removed;

-- ApproveSenderAsNew lets a sender in as someone new: a channel user of their
-- own, a member of the channel's team with the role given, bound to the
-- sender, and the sender out of the queue, all in one statement as
-- ApproveSender is. The user is keyed on the channel and the ref, so
-- approving the same sender again finds the same user, and neither its name
-- nor a role since changed is overwritten.
--
-- The binding is written while the ref is unbound, already bound to this
-- user, or still held by expected_holder, the holder the handler checked
-- the caller may take it from. A parent row and its child inserted in one
-- statement pass the foreign key: Postgres checks it at the end of the
-- statement. 0 linked is a 409, and nothing else was written that the
-- caller can see: the user and membership of a refused approval are the
-- ones a later approval of this sender would make anyway.
-- name: ApproveSenderAsNew :one
WITH made AS (
    INSERT INTO users (issuer, subject, name)
    VALUES ('openarity:channel', sqlc.arg('channel_id')::uuid::text || ':' || sqlc.arg('sender_ref')::text,
            sqlc.arg('name')::text)
    ON CONFLICT (issuer, subject) DO UPDATE SET updated_at = users.updated_at
    RETURNING id
), joined AS (
    INSERT INTO team_members (team_id, user_id, role)
    SELECT c.team_id, made.id, sqlc.arg('role')::text
    FROM made, channels c
    WHERE c.id = sqlc.arg('channel_id')::uuid
    ON CONFLICT (team_id, user_id) DO NOTHING
), linked AS (
    INSERT INTO channel_senders (channel_id, sender_ref, user_id)
    SELECT sqlc.arg('channel_id')::uuid, sqlc.arg('sender_ref')::text, made.id FROM made
    ON CONFLICT (channel_id, sender_ref) DO UPDATE SET user_id = EXCLUDED.user_id
    WHERE channel_senders.user_id = EXCLUDED.user_id
       OR channel_senders.user_id IS NOT DISTINCT FROM sqlc.narg('expected_holder')::uuid
    RETURNING 1
), admitted AS (
    INSERT INTO notices (team_id, channel_id, sender_ref, sender_name, session_ref, session_kind, kind)
    SELECT c.team_id, p.channel_id, p.sender_ref, p.sender_name,
           p.notice_session_ref, p.notice_session_kind, 'admitted'
    FROM pending_senders p
    JOIN channels c ON c.id = p.channel_id
    WHERE p.channel_id = sqlc.arg('channel_id')::uuid
      AND p.sender_ref = sqlc.arg('sender_ref')::text
      AND p.notified_at IS NOT NULL
      AND c.notify_strangers
      AND EXISTS (SELECT 1 FROM linked)
    RETURNING id
), dequeued AS (
    DELETE FROM pending_senders
    WHERE pending_senders.channel_id = sqlc.arg('channel_id')::uuid
      AND pending_senders.sender_ref = sqlc.arg('sender_ref')::text
      AND EXISTS (SELECT 1 FROM linked)
)
SELECT (SELECT count(*) FROM linked) AS linked,
       COALESCE((SELECT id FROM admitted), 0)::bigint AS admitted,
       (SELECT id FROM made)::uuid AS user_id;

-- FindPendingSenderName is the name a waiting sender gave, for a channel
-- user approved with none of its own.
-- name: FindPendingSenderName :one
SELECT sender_name FROM pending_senders WHERE channel_id = $1 AND sender_ref = $2;

-- DeleteLostChannelUser deletes a channel user no sender speaks as any more:
-- one whose last binding was moved to somebody else or removed. Nobody can
-- log in as one, so with no binding it is a member of the team that can do
-- nothing. A user who logs in is never touched.
-- name: DeleteLostChannelUser :execrows
DELETE FROM users u
WHERE u.id = $1 AND u.issuer = 'openarity:channel'
  AND NOT EXISTS (SELECT 1 FROM channel_senders cs WHERE cs.user_id = u.id);
