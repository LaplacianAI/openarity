-- The reply to a channel run, written in the transaction that finishes it. It
-- answers the newest message the run read, whether it came in as the input or
-- as a steer. A message that arrived after the run's last request was steered
-- into it and never read: it goes to the follow-up run, and unread_since, when
-- the first such steer was written, keeps this reply from quoting it. A message
-- and its steer share a transaction, so they share now(). A run that does not
-- answer a channel, or whose session came from none, inserts nothing and the
-- caller gets pgx.ErrNoRows. So does a custom channel with no reply URL: it
-- takes messages in and has nowhere to post back to, and a reply written for
-- it could only be given up.
-- name: InsertReply :one
INSERT INTO replies (team_id, run_id, session_id, channel_id, in_reply_to, kind, text)
SELECT r.team_id, r.id, r.session_id, s.channel_id,
       (SELECT m.id FROM messages m WHERE m.session_id = r.session_id AND NOT m.answer
          AND (sqlc.narg('unread_since')::timestamptz IS NULL OR m.received_at < sqlc.narg('unread_since')::timestamptz)
        ORDER BY m.received_at DESC, m.id DESC LIMIT 1),
       sqlc.arg('kind')::text, sqlc.arg('text')::text
FROM runs r
JOIN sessions s ON s.id = r.session_id
JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid AND r.reply_to_channel
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
RETURNING *;

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
          -- A socket's channels are sent by the process holding the socket:
          -- it claims those it holds, and every other sender leaves them.
          AND CASE WHEN sqlc.arg('held_only')::bool
                   THEN d.channel_id = ANY(sqlc.arg('held')::uuid[])
                   ELSE NOT EXISTS (SELECT 1 FROM channels s
                                    WHERE s.id = d.channel_id AND s.provider = ANY(sqlc.arg('sockets')::text[]))
              END
          AND NOT EXISTS (
              SELECT 1 FROM replies o
              WHERE o.session_id = d.session_id AND o.status = 'pending' AND o.id < d.id)
        ORDER BY d.next_attempt_at, d.id
        LIMIT sqlc.arg('batch_size')
        FOR UPDATE SKIP LOCKED)
    RETURNING r.*
)
SELECT c.id, c.run_id, c.team_id, c.channel_id, c.kind, c.text, c.attempts, c.parts_sent, c.event_seq, c.detail,
       ch.provider, ch.reply_url,
       s.provider_ref AS session_ref, s.kind AS session_kind,
       m.external_id AS in_reply_to
FROM claimed c
JOIN channels ch ON ch.id = c.channel_id
JOIN sessions s ON s.id = c.session_id
LEFT JOIN messages m ON m.id = c.in_reply_to
ORDER BY c.id;

-- A part went out. Matched on attempts like every mark, so a sender whose
-- lease ran out cannot move another's progress. The first part says where it
-- landed, for a platform that names it (Slack's "<channel>:<ts>"); a later
-- part keeps what the first said.
-- name: ReplyPartSent :execrows
UPDATE replies SET parts_sent = sqlc.arg('parts_sent'),
                   posted_ref = coalesce(posted_ref, sqlc.narg('posted_ref'))
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

-- A file reply's lease, pushed out to what sending the file may take before
-- it is sent: a large one outlasts the claim's. Matched on attempts, so a
-- lease another sender took since is not extended; no row says so.
-- name: ExtendReplyLease :execrows
UPDATE replies
SET next_attempt_at = now() + make_interval(secs => sqlc.arg('lease_seconds')::float8)
WHERE id = sqlc.arg('id') AND status = 'pending' AND attempts = sqlc.arg('attempts');

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

-- What became of each of these runs' answers. A run that answers no channel
-- has none and is not in the result; its events are not its answer. Rides
-- replies_one_per_run.
-- name: ListRunReplies :many
SELECT run_id, kind, status, attempts, last_error, sent_at, next_attempt_at
FROM replies
WHERE run_id = ANY(sqlc.arg('run_ids')::uuid[]) AND kind IN ('answer', 'apology');

-- Whether a run's steps are sent to its conversation as they happen: a run
-- that answers a channel whose reply_mode is events. Only a top-level run
-- answers a channel (runs_reply_from_a_root), so a delegated run's steps are
-- its parent's to tell.
-- name: RunShowsEvents :one
SELECT EXISTS (
    SELECT 1 FROM runs r
    JOIN sessions s ON s.id = r.session_id
    JOIN channels ch ON ch.id = s.channel_id
    WHERE r.id = sqlc.arg('run_id')::uuid
      AND r.reply_to_channel
      AND ch.reply_mode = 'events'
      AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL))::bool;

-- One step of a run, sent to its conversation. Only for a run RunShowsEvents
-- holds for, read again here so a channel switched to final since the run
-- began is sent no more. It quotes nothing. Written again for the same run
-- event, as a retried write is, it is still one row.
-- name: InsertEventReply :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text, event_seq)
SELECT r.team_id, r.id, r.session_id, s.channel_id, 'event', sqlc.arg('text')::text, sqlc.arg('event_seq')::integer
FROM runs r
JOIN sessions s ON s.id = r.session_id
JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid
  AND r.reply_to_channel
  AND ch.reply_mode = 'events'
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (run_id, event_seq) WHERE event_seq IS NOT NULL DO NOTHING;

-- name: RunTriggerWord :one
-- The word a run's channel is called by: empty when it has none, or when the
-- run has no channel, so a run is never failed for asking.
SELECT COALESCE((
    SELECT ch.trigger_word FROM runs r
    JOIN sessions s ON s.id = r.session_id
    JOIN channels ch ON ch.id = s.channel_id
    WHERE r.id = sqlc.arg('run_id')::uuid), '')::text;

-- A run that waits for a person asks them in its conversation: what the
-- agent said and the brain's footer, with detail naming the requests for the
-- platform's buttons. Keyed by the waiting event, so a run that asks again
-- later asks again, and a park written twice is one prompt. Only a top-level
-- run answers a channel; one whose session came from none, or a custom
-- channel with no reply URL (nowhere to post), inserts nothing.
-- It answers the newest message the run read, as its answer will.
-- name: InsertApprovalPrompt :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, in_reply_to, kind, text, event_seq, detail)
SELECT r.team_id, r.id, r.session_id, s.channel_id,
       (SELECT m.id FROM messages m WHERE m.session_id = r.session_id
        ORDER BY m.received_at DESC, m.id DESC LIMIT 1),
       'approval', sqlc.arg('text')::text, sqlc.arg('event_seq')::integer, sqlc.arg('detail')::jsonb
FROM runs r
JOIN sessions s ON s.id = r.session_id
JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid AND r.reply_to_channel
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (run_id, event_seq) WHERE event_seq IS NOT NULL DO NOTHING;

-- The brain's short word on an answer given in the chat, on the run that
-- asked, answering the message (or the tap) it is about: one at most for
-- each, so a delivery the platform repeats is told once. A custom channel
-- with no reply URL is told nothing, as InsertReply: it has nowhere to post.
-- name: InsertNotice :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, in_reply_to, kind, text, detail)
SELECT sqlc.arg('team_id')::uuid, sqlc.arg('run_id')::uuid, sqlc.arg('session_id')::uuid,
       ch.id, sqlc.arg('message_id')::uuid, 'notice', sqlc.arg('text')::text,
       sqlc.narg('detail')::jsonb
FROM channels ch
WHERE ch.id = sqlc.arg('channel_id')::uuid
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (in_reply_to) WHERE kind = 'notice' DO NOTHING;

-- A file a run hands its person (send_file): written for a run that answers a
-- channel which can be posted back to, as InsertReply is, and only for a file
-- of the run's own session, so another conversation's id inserts nothing. It
-- quotes nothing, as a step does. text is the file's name and detail its
-- caption. A call made again is still one reply.
-- name: InsertFileReply :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text, detail, attachment_id, call_id)
SELECT r.team_id, r.id, r.session_id, s.channel_id, 'file', a.filename,
       sqlc.narg('detail')::jsonb, a.id, sqlc.arg('call_id')::text
FROM runs r
JOIN sessions s ON s.id = r.session_id
JOIN channels ch ON ch.id = s.channel_id
JOIN attachments a ON a.id = sqlc.arg('attachment_id')::uuid AND a.session_id = r.session_id
WHERE r.id = sqlc.arg('run_id')::uuid
  AND r.reply_to_channel
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (run_id, call_id) WHERE kind = 'file' DO NOTHING;

-- Whether a call's file reply was written: what InsertFileReply writing
-- none means, a call made again, or a channel that can no longer be posted
-- to. Rides replies_one_per_file_call.
-- name: FileReplyExists :one
SELECT EXISTS (
    SELECT 1 FROM replies
    WHERE run_id = sqlc.arg('run_id')::uuid AND call_id = sqlc.arg('call_id')::text AND kind = 'file')::bool;

-- How many files a run has sent, each call once whether its reply or its
-- event was written: a run resumed holds to the ten it may send, counting
-- those before.
-- name: CountRunFilesSent :one
SELECT count(*) FROM (
    SELECT call_id FROM replies WHERE run_id = sqlc.arg('run_id')::uuid AND kind = 'file'
    UNION
    SELECT payload->>'tool_call_id' FROM run_events WHERE run_id = sqlc.arg('run_id')::uuid AND kind = 'file_sent'
) AS sent;

-- Where a conversation's files can reach. One read on its stream, the
-- dashboard's or the API's, takes them there: it has no channel, or the
-- team's dashboard channel, which nothing posts to. Any other is its
-- channel's provider, and whether that channel can be posted back to.
-- name: GetSessionFileReach :one
SELECT (s.channel_id IS NULL OR ch.provider = 'dashboard')::bool AS streamed,
       COALESCE(ch.provider, '')::text AS provider,
       (ch.provider IS NOT NULL AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL))::bool AS postable
FROM sessions s
LEFT JOIN channels ch ON ch.id = s.channel_id
WHERE s.id = sqlc.arg('session_id')::uuid;

-- The file a reply sends, read by the sender just before it posts it.
-- name: GetReplyFile :one
SELECT a.team_id, a.object_key, a.filename, a.media_type, a.size_bytes
FROM replies r
JOIN attachments a ON a.id = r.attachment_id AND a.session_id = r.session_id
WHERE r.id = sqlc.arg('id');

-- What a top-level run said as it parked to wait for sandbox room, sent as a
-- step is but whatever the channel's reply mode: nobody is asked anything,
-- and without it the person hears nothing until the run carries on. Named by
-- its waiting event, so a park retried is sent once.
-- name: InsertWaitingReply :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text, event_seq)
SELECT r.team_id, r.id, r.session_id, s.channel_id, 'event', sqlc.arg('text')::text, sqlc.arg('event_seq')::integer
FROM runs r
JOIN sessions s ON s.id = r.session_id
JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid
  AND r.reply_to_channel
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (run_id, event_seq) WHERE event_seq IS NOT NULL DO NOTHING;

-- Whether a run can tell its person something while it works (reply), and
-- how: its tree answers a person's message, on the dashboard or the API's
-- stream, or on a channel it can post to. A schedule's or a watcher's
-- conversation answers nobody who is waiting, so it says no; a delegate
-- answers as its root does. streamed is a conversation read on its stream,
-- whose event is the delivery.
-- name: GetReplyReach :one
SELECT (s.schedule_id IS NULL AND s.watcher_id IS NULL
        AND (s.channel_id IS NULL OR ch.provider = 'dashboard'
             OR (root.reply_to_channel AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL))))::bool AS answers,
       (s.channel_id IS NULL OR ch.provider = 'dashboard')::bool AS streamed
FROM runs r
JOIN runs root ON root.id = r.root_run_id
JOIN sessions s ON s.id = root.session_id
LEFT JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid;

-- What a run, or a delegate of it, told its person, sent to the root's
-- channel as a step is, whatever its reply mode: the person is waiting on it,
-- as on a waiting line. Named by its said event, so a write retried is one
-- reply. Read again here, so a conversation that can no longer be posted to
-- is sent nothing.
-- name: InsertSaidReply :execrows
INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text, event_seq)
SELECT r.team_id, r.id, root.session_id, s.channel_id, 'event', sqlc.arg('text')::text, sqlc.arg('event_seq')::integer
FROM runs r
JOIN runs root ON root.id = r.root_run_id
JOIN sessions s ON s.id = root.session_id
JOIN channels ch ON ch.id = s.channel_id
WHERE r.id = sqlc.arg('run_id')::uuid
  AND root.reply_to_channel
  AND (ch.provider <> 'custom' OR ch.reply_url IS NOT NULL)
ON CONFLICT (run_id, event_seq) WHERE event_seq IS NOT NULL DO NOTHING;

-- How often a run's tree has told its person something, for reply's limit:
-- every delegate's counts, so a tree of five cannot send fifty.
-- name: CountSaidInTree :one
SELECT count(*) FROM run_events e
JOIN runs r ON r.id = e.run_id
WHERE r.root_run_id = sqlc.arg('root_id')::uuid AND e.kind = 'said';

-- What a delegate told its person, oldest first, for the report its parent
-- reads: so the parent does not say it again.
-- name: ListRunSaid :many
SELECT (payload->>'text')::text AS text FROM run_events
WHERE run_id = sqlc.arg('run_id')::uuid AND kind = 'said'
ORDER BY seq;
