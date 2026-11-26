-- Schedules, and what each fire did. See internal/triggers.

-- name: CreateSchedule :one
INSERT INTO schedules (
    id, team_id, created_by, agent_id, hub, name, objective, kind, rule, anchor, timezone,
    status, next_fire_at, proposed_run, proposed_in
) VALUES (
    sqlc.arg('id'), sqlc.arg('team_id'), sqlc.arg('created_by')::uuid, sqlc.narg('agent_id'), sqlc.arg('hub'),
    sqlc.arg('name'), sqlc.arg('objective'), sqlc.arg('kind'), sqlc.arg('rule'), sqlc.arg('anchor'),
    sqlc.arg('timezone'), sqlc.arg('status'), sqlc.narg('next_fire_at'),
    sqlc.narg('proposed_run'), sqlc.narg('proposed_in')
)
RETURNING *;

-- A schedule's or a watcher's own conversation: on the chat it answers in,
-- or the team's dashboard channel, and outside the index a message finds its
-- conversation by.
-- name: CreateTriggerSession :one
INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id, schedule_id, watcher_id)
VALUES (
    sqlc.arg('team_id'), sqlc.arg('channel_id')::uuid, sqlc.arg('provider_ref')::text,
    sqlc.arg('kind'), sqlc.narg('user_id'), sqlc.narg('schedule_id'), sqlc.narg('watcher_id')
)
RETURNING *;

-- Serialises a team's creates and fires, so two cannot both slip under a
-- limit counted before either wrote. Held to the end of the transaction.
-- name: LockTeamTriggers :exec
SELECT pg_advisory_xact_lock(hashtextextended('triggers:' || sqlc.arg('team_id')::uuid::text, 0));

-- Schedules and watchers that count toward the limits, the team's and one
-- member's: everything not done or deleted.
-- name: CountLiveTriggers :one
SELECT
    ((SELECT count(*) FROM schedules s
      WHERE s.team_id = sqlc.arg('team_id') AND s.status IN ('proposed', 'active', 'paused'))
   + (SELECT count(*) FROM watchers w
      WHERE w.team_id = sqlc.arg('team_id') AND w.status IN ('proposed', 'waiting_secret', 'active', 'paused'))
    )::int AS team,
    ((SELECT count(*) FROM schedules s
      WHERE s.team_id = sqlc.arg('team_id') AND s.created_by = sqlc.arg('maker')::uuid
        AND s.status IN ('proposed', 'active', 'paused'))
   + (SELECT count(*) FROM watchers w
      WHERE w.team_id = sqlc.arg('team_id') AND w.created_by = sqlc.arg('maker')::uuid
        AND w.status IN ('proposed', 'waiting_secret', 'active', 'paused'))
    )::int AS maker;

-- name: LockSchedule :one
SELECT * FROM schedules
WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id') AND status <> 'deleted'
FOR UPDATE;

-- One of a maker's own, by name: how a person or an agent picks one.
-- name: ScheduleIDByName :one
SELECT id FROM schedules
WHERE team_id = sqlc.arg('team_id') AND created_by = sqlc.arg('maker')::uuid
  AND lower(name) = lower(sqlc.arg('name')) AND status <> 'deleted';

-- Every column a change may move, written back whole after the caller locked
-- the row and worked out what it becomes.
-- name: SaveSchedule :one
UPDATE schedules SET
    created_by    = sqlc.narg('created_by'),
    agent_id      = sqlc.narg('agent_id'),
    hub           = sqlc.arg('hub'),
    name          = sqlc.arg('name'),
    objective     = sqlc.arg('objective'),
    kind          = sqlc.arg('kind'),
    rule          = sqlc.arg('rule'),
    anchor        = sqlc.arg('anchor'),
    timezone      = sqlc.arg('timezone'),
    status        = sqlc.arg('status'),
    paused_reason = sqlc.narg('paused_reason'),
    deleted_at    = sqlc.narg('deleted_at'),
    next_fire_at  = sqlc.narg('next_fire_at'),
    -- A fire that went through, and any change, ends a wait to retry.
    retry_at      = NULL,
    last_fired_at = sqlc.narg('last_fired_at'),
    last_local    = sqlc.narg('last_local'),
    consecutive_failures = sqlc.arg('consecutive_failures'),
    updated_at    = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- A schedule as it is shown: with its conversation and what its newest fire did.
-- name: GetScheduleView :one
SELECT sqlc.embed(s), ss.id AS session_id, ss.channel_id AS session_channel_id,
       coalesce((SELECT f.outcome FROM schedule_fires f WHERE f.schedule_id = s.id
                 ORDER BY f.scheduled_for DESC LIMIT 1), '')::text AS last_outcome
FROM schedules s
JOIN sessions ss ON ss.schedule_id = s.id
WHERE s.id = sqlc.arg('id') AND s.team_id = sqlc.arg('team_id');

-- A team's schedules, newest first, a page at a time. Only the viewer's own
-- unless everyone's is asked for by one who may see them; deleted ones only
-- when asked for by status.
-- name: ListScheduleViews :many
SELECT sqlc.embed(s), ss.id AS session_id, ss.channel_id AS session_channel_id,
       coalesce((SELECT f.outcome FROM schedule_fires f WHERE f.schedule_id = s.id
                 ORDER BY f.scheduled_for DESC LIMIT 1), '')::text AS last_outcome
FROM schedules s
JOIN sessions ss ON ss.schedule_id = s.id
WHERE s.team_id = sqlc.arg('team_id')
  AND (sqlc.arg('everyone')::bool OR s.created_by = sqlc.arg('viewer')::uuid)
  AND (CASE WHEN cardinality(sqlc.arg('statuses')::text[]) = 0 THEN s.status <> 'deleted'
            ELSE s.status = ANY(sqlc.arg('statuses')::text[]) END)
  AND (sqlc.narg('after_created')::timestamptz IS NULL
       OR (s.created_at, s.id) < (sqlc.narg('after_created')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY s.created_at DESC, s.id DESC
LIMIT sqlc.arg('page_size');

-- name: GetTriggerSessionOfSchedule :one
SELECT * FROM sessions WHERE schedule_id = $1;

-- A trigger's conversation ends when it is deleted; it stays, with its runs.
-- name: CloseTriggerSession :exec
UPDATE sessions SET status = 'closed'
WHERE (schedule_id = sqlc.narg('schedule_id')::uuid OR watcher_id = sqlc.narg('watcher_id')::uuid);

-- How many schedules are due: what the tick fans out by.
-- name: CountDueSchedules :one
SELECT count(*)::int FROM schedules
WHERE status = 'active' AND next_fire_at <= sqlc.arg('now')::timestamptz
  AND (retry_at IS NULL OR retry_at <= sqlc.arg('now')::timestamptz);

-- One due schedule, taken for the rest of the transaction. Skipped while
-- another worker holds it, so workers split the due ones between them.
-- name: ClaimDueSchedule :one
SELECT * FROM schedules
WHERE status = 'active' AND next_fire_at <= sqlc.arg('now')::timestamptz
  AND (retry_at IS NULL OR retry_at <= sqlc.arg('now')::timestamptz)
ORDER BY next_fire_at
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- A fire, once per scheduled time. No row written is a time already fired.
-- name: InsertScheduleFire :execrows
INSERT INTO schedule_fires (schedule_id, team_id, scheduled_for, fired_at, outcome, run_id)
VALUES (sqlc.arg('schedule_id'), sqlc.arg('team_id'), sqlc.arg('scheduled_for'), sqlc.arg('fired_at'),
        sqlc.arg('outcome'), sqlc.narg('run_id'))
ON CONFLICT (schedule_id, scheduled_for) DO NOTHING;

-- The run a fire started, once the fire's row holds its time: the row goes
-- first, so a time already fired starts nothing.
-- name: SetFireRun :exec
UPDATE schedule_fires SET run_id = sqlc.arg('run_id')
WHERE schedule_id = sqlc.arg('schedule_id') AND scheduled_for = sqlc.arg('scheduled_for');

-- The agent a fire is about to start, held so it cannot be deleted before
-- the run names it. No row is an agent gone.
-- name: HoldAgent :one
SELECT id FROM agents WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id') FOR KEY SHARE;

-- Runs a team's schedules and watchers started in the hour before now: what
-- the hourly cap holds. A webhook's deliveries are checks with runs.
-- name: CountTriggerRunsInHour :one
SELECT
    ((SELECT count(*) FROM schedule_fires f
      WHERE f.team_id = sqlc.arg('team_id') AND f.run_id IS NOT NULL
        AND f.fired_at > sqlc.arg('now')::timestamptz - interval '1 hour')
   + (SELECT count(*) FROM watcher_checks c
      WHERE c.team_id = sqlc.arg('team_id') AND c.run_id IS NOT NULL
        AND c.checked_at > sqlc.arg('now')::timestamptz - interval '1 hour')
    )::int AS runs;

-- A run a schedule or a watcher starts: in its own conversation, as its maker,
-- by its agent or the hub. Nothing is asked of it by a message, so it names
-- none. A session that is no trigger's, or an agent that is not the team's,
-- inserts nothing.
-- name: CreateTriggeredRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input,
                  token_budget, hub, builder, reply_to_channel, transcript)
SELECT sqlc.arg('id')::uuid, ss.team_id, ss.id, sqlc.arg('id')::uuid,
       a.id, coalesce(a.name, 'orchestrator'), sqlc.arg('user_id')::uuid, sqlc.arg('input')::text,
       a.token_budget, sqlc.arg('hub')::bool, false,
       -- The dashboard's answer is its stream: there is nothing to post it to.
       ch.provider <> 'dashboard',
       'session:' || ss.id::text
FROM sessions ss
JOIN channels ch ON ch.id = ss.channel_id
LEFT JOIN team_settings ts ON ts.team_id = ss.team_id
LEFT JOIN agents a ON a.team_id = ss.team_id
                  AND a.id = CASE WHEN sqlc.arg('hub')::bool THEN ts.hub_agent_id
                                  ELSE sqlc.narg('agent_id')::uuid END
WHERE ss.id = sqlc.arg('session_id') AND ss.team_id = sqlc.arg('team_id')
  AND (ss.schedule_id IS NOT NULL OR ss.watcher_id IS NOT NULL)
  AND (sqlc.arg('hub')::bool OR a.id IS NOT NULL)
RETURNING *;

-- How a fired run ended, on its fire, once: only a fire still waiting on its
-- run takes it. No row is a run no schedule started, or an end already
-- recorded, and neither is counted again.
-- name: SetFireOutcome :one
UPDATE schedule_fires SET outcome = sqlc.arg('outcome')
WHERE run_id = sqlc.arg('run_id') AND outcome IN ('started', 'late', 'catch_up')
RETURNING schedule_id;

-- A fired run's end counted on its schedule: a success clears the failures,
-- a failure adds one, and the fifth in a row pauses a schedule still active.
-- A cancelled run is neither.
-- name: CountScheduleRun :one
UPDATE schedules SET
    consecutive_failures = CASE
        WHEN sqlc.arg('failed')::bool THEN consecutive_failures + 1
        WHEN sqlc.arg('succeeded')::bool THEN 0
        ELSE consecutive_failures END,
    status = CASE
        WHEN sqlc.arg('failed')::bool AND status = 'active'
             AND consecutive_failures + 1 >= sqlc.arg('pause_after')::int THEN 'paused'
        ELSE status END,
    paused_reason = CASE
        WHEN sqlc.arg('failed')::bool AND status = 'active'
             AND consecutive_failures + 1 >= sqlc.arg('pause_after')::int THEN 'failing'
        ELSE paused_reason END,
    next_fire_at = CASE
        WHEN sqlc.arg('failed')::bool AND status = 'active'
             AND consecutive_failures + 1 >= sqlc.arg('pause_after')::int THEN NULL
        ELSE next_fire_at END,
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING status, paused_reason;

-- A fire that could not even be tried, an error rather than a refusal, waits
-- until retry_at and is then tried again for the time it was due, so the next
-- claim takes another schedule rather than this one again. It counts as a
-- failed run when it failed on its own, or when the database's passing
-- trouble has not passed since stuck_before: next_fire_at stays on the time
-- it was due while it retries, so that is how long it has been stuck. The
-- fifth count in a row pauses the schedule, with no retry left waiting.
-- name: DeferFailedFire :one
UPDATE schedules s SET
    consecutive_failures = s.consecutive_failures + CASE WHEN c.counts THEN 1 ELSE 0 END,
    status        = CASE WHEN c.pauses THEN 'paused' ELSE s.status END,
    paused_reason = CASE WHEN c.pauses THEN 'failing' END,
    next_fire_at  = CASE WHEN c.pauses THEN NULL ELSE s.next_fire_at END,
    retry_at      = CASE WHEN c.pauses THEN NULL ELSE sqlc.arg('retry_at')::timestamptz END,
    updated_at    = now()
FROM (
    SELECT id, counts, counts AND consecutive_failures + 1 >= sqlc.arg('pause_after')::int AS pauses
    FROM (
        SELECT j.id, j.consecutive_failures,
               sqlc.arg('counts')::bool OR j.next_fire_at < sqlc.arg('stuck_before')::timestamptz AS counts
        FROM schedules j WHERE j.id = sqlc.arg('id')
    ) judged
) c
WHERE s.id = c.id AND s.status = 'active'
RETURNING s.status;

-- A schedule's fires, newest first, a page at a time.
-- name: ListScheduleFires :many
SELECT * FROM schedule_fires
WHERE schedule_id = sqlc.arg('schedule_id') AND team_id = sqlc.arg('team_id')
  AND (sqlc.narg('before')::timestamptz IS NULL OR scheduled_for < sqlc.narg('before')::timestamptz)
ORDER BY scheduled_for DESC
LIMIT sqlc.arg('page_size');

-- What a maker has not yet seen of their schedules: what went wrong wherever
-- it answers, and what was answered in a conversation of its own, where
-- nobody else would see it. A silent run tells nobody.
-- name: ListScheduleNews :many
SELECT f.schedule_id, s.name, ss.id AS session_id, f.scheduled_for, f.fired_at, f.outcome, f.run_id
FROM schedule_fires f
JOIN schedules s ON s.id = f.schedule_id
JOIN sessions ss ON ss.schedule_id = s.id
JOIN channels ch ON ch.id = ss.channel_id
WHERE s.team_id = sqlc.arg('team_id') AND s.created_by = sqlc.arg('maker')::uuid
  AND s.status <> 'deleted' AND f.seen_at IS NULL
  AND (f.outcome IN ('failed', 'skipped_busy', 'skipped_cap', 'skipped_maker', 'skipped_target', 'missed')
       OR (f.outcome = 'answered' AND ch.provider = 'dashboard'))
ORDER BY f.fired_at DESC, f.schedule_id
LIMIT sqlc.arg('page_size');

-- name: MarkScheduleSeen :execrows
UPDATE schedule_fires f SET seen_at = now()
FROM schedules s
WHERE s.id = f.schedule_id AND f.schedule_id = sqlc.arg('schedule_id')
  AND s.team_id = sqlc.arg('team_id') AND s.created_by = sqlc.arg('maker')::uuid
  AND f.seen_at IS NULL;

-- Proposals nobody confirmed within the hour are deleted as any schedule is:
-- marked so, their conversation closed and kept.
-- name: ExpireProposedSchedules :execrows
WITH gone AS (
    UPDATE schedules SET status = 'deleted', deleted_at = now(), updated_at = now()
    WHERE status = 'proposed' AND created_at < sqlc.arg('before')
    RETURNING id
)
UPDATE sessions SET status = 'closed'
WHERE schedule_id IN (SELECT id FROM gone);

-- Each schedule keeps its newest fires; the rest go a batch at a time.
-- name: PruneScheduleFires :execrows
DELETE FROM schedule_fires f
USING (
    SELECT schedule_id, scheduled_for FROM (
        SELECT schedule_id, scheduled_for,
               row_number() OVER (PARTITION BY schedule_id ORDER BY scheduled_for DESC) AS n
        FROM schedule_fires
    ) ranked
    WHERE n > sqlc.arg('keep')::int
    LIMIT sqlc.arg('batch')
) old
WHERE f.schedule_id = old.schedule_id AND f.scheduled_for = old.scheduled_for;

-- When the message a run answers arrived: a proposal is confirmed only by one
-- sent after it.
-- name: MessageReceivedAt :one
SELECT received_at FROM messages WHERE id = sqlc.arg('id') AND session_id = sqlc.arg('session_id');

-- A schedule taken over by an admin: its own dashboard conversation is with
-- them from now on.
-- name: SetTriggerSessionUser :exec
UPDATE sessions SET user_id = sqlc.arg('user_id')::uuid
WHERE schedule_id = sqlc.arg('schedule_id')::uuid AND kind = 'direct'
  AND channel_id = (SELECT id FROM channels WHERE team_id = sessions.team_id AND provider = 'dashboard');
