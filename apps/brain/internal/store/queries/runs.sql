-- The agent's team is checked by the SELECT: an agent of another team inserts
-- nothing, and the caller gets pgx.ErrNoRows.
-- name: CreateRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input, token_budget, transcript)
SELECT sqlc.arg('id')::uuid, a.team_id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       a.id, a.name, sqlc.narg('user_id')::uuid, sqlc.arg('input')::text, a.token_budget,
       'session:' || sqlc.arg('session_id')::uuid::text
FROM agents a
WHERE a.id = sqlc.arg('agent_id') AND a.team_id = sqlc.arg('team_id')
RETURNING *;

-- The team's hub at the root: its chosen agent, or the built-in orchestrator
-- when none is set. The agent is read in this statement, so a hub deleted a
-- moment before is already the orchestrator. No team, no row.
-- name: CreateHubRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input, token_budget, hub, transcript)
SELECT sqlc.arg('id')::uuid, t.id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       a.id, coalesce(a.name, 'orchestrator'), sqlc.narg('user_id')::uuid, sqlc.arg('input')::text,
       a.token_budget, true, 'session:' || sqlc.arg('session_id')::uuid::text
FROM teams t
LEFT JOIN team_settings s ON s.team_id = t.id
LEFT JOIN agents a ON a.id = s.hub_agent_id AND a.team_id = t.id
WHERE t.id = sqlc.arg('team_id')::uuid
RETURNING *;

-- The session rule from ListSessionsByTeam, applied to the run's session.
-- name: GetRunVisible :one
SELECT * FROM runs r
WHERE r.id = sqlc.arg('id') AND r.team_id = sqlc.arg('team_id')
  AND EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = r.session_id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid));

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- Queued: take it. Running and already ours, after a takeover: carry on.
-- Anything else (cancelled while queued, finished, someone else's): no row.
-- remaining_seconds is the wall-clock budget left, on the database's clock.
-- name: ClaimRun :one
UPDATE runs SET
    status       = 'running',
    worker       = sqlc.arg('worker')::text,
    heartbeat_at = now(),
    started_at   = coalesce(started_at, now())
WHERE id = sqlc.arg('id')
  AND (status = 'queued' OR (status = 'running' AND worker = sqlc.arg('worker')::text))
RETURNING *,
    extract(epoch FROM started_at
        + make_interval(secs => sqlc.arg('wall_clock_seconds')::float8 + waited_seconds) - now())::float8
        AS remaining_seconds;

-- No row means this worker no longer holds the run.
-- name: HeartbeatRun :one
UPDATE runs SET heartbeat_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running'
RETURNING cancel_requested_at;

-- Counted as it is spent, so a resumed run carries on from the same total.
-- spent is what the run's budget counts (run_spent: a cached token as a
-- tenth). A provider counting more cached than it read, or less than none,
-- is held between none and what it read.
-- name: AddRunUsage :one
UPDATE runs SET
    input_tokens        = input_tokens  + sqlc.arg('input_tokens')::bigint,
    output_tokens       = output_tokens + sqlc.arg('output_tokens')::bigint,
    cached_input_tokens = cached_input_tokens + greatest(0,
        least(sqlc.arg('cached_input_tokens')::bigint, sqlc.arg('input_tokens')::bigint))
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running'
RETURNING run_spent(input_tokens, cached_input_tokens, output_tokens)::bigint AS spent;

-- name: FinishRun :execrows
UPDATE runs SET
    status      = sqlc.arg('status')::text,
    output      = sqlc.narg('output'),
    error       = sqlc.narg('error'),
    finished_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running';

-- Queued: cancelled now, since no worker holds it. Running: flagged for the
-- worker's next heartbeat. Finished: no row, which the handler answers 409.
-- A queued run can still have children running, left by a worker that
-- stopped mid-delegation; it will never run again to end them, so they are
-- cancelled with it, at any depth. A waiting run holds no worker either, and
-- is cancelled at once as a queued one is, with its children running or
-- waiting beneath it. A running run's children are its own to end.
-- name: RequestRunCancel :one
WITH RECURSIVE below AS (
    SELECT r.id FROM runs r
    WHERE r.parent_run_id = sqlc.arg('id')::uuid
      AND EXISTS (SELECT 1 FROM runs q WHERE q.id = sqlc.arg('id')::uuid AND q.status IN ('queued', 'waiting'))
    UNION ALL
    SELECT r.id FROM runs r JOIN below b ON r.parent_run_id = b.id
), ended AS (
    UPDATE runs SET status = 'cancelled', finished_at = now(),
                    cancel_requested_at = coalesce(cancel_requested_at, now())
    WHERE runs.id IN (SELECT below.id FROM below) AND runs.status IN ('running', 'waiting')
    RETURNING runs.id
)
UPDATE runs SET
    cancel_requested_at = coalesce(cancel_requested_at, now()),
    status      = CASE WHEN status IN ('queued', 'waiting') THEN 'cancelled' ELSE status END,
    finished_at = CASE WHEN status IN ('queued', 'waiting') THEN now() ELSE finished_at END
WHERE runs.id = sqlc.arg('id')::uuid AND runs.status IN ('queued', 'running', 'waiting')
RETURNING *, (SELECT coalesce(array_agg(ended.id), '{}') FROM ended)::uuid[] AS ended_children;

-- name: ListUnstartedRuns :many
SELECT id, attempt FROM runs
WHERE status = 'queued'
  AND created_at < now() - make_interval(secs => sqlc.arg('older_than_seconds')::float8)
ORDER BY created_at
LIMIT 100;

-- One winner per stale run: the row is locked while it is handed back, and
-- the old worker's next heartbeat finds it no longer running and stops.
-- A child is never taken over: only its parent decides what becomes of it.
-- name: TakeOverStaleRuns :many
UPDATE runs SET status = 'queued', worker = NULL, heartbeat_at = NULL
WHERE id IN (
    SELECT id FROM runs
    WHERE status = 'running' AND parent_run_id IS NULL
      AND heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8)
    ORDER BY heartbeat_at
    LIMIT 20
    FOR UPDATE SKIP LOCKED)
RETURNING id;

-- Top-level runs only: a delegate's run shows in its parent's trace.
-- name: ListRunsBySession :many
SELECT * FROM runs
WHERE session_id = sqlc.arg('session_id')
  AND id = root_run_id
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (created_at, id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- A team's run history: root runs only, in sessions the caller can see by the
-- rule GetRunVisible uses. A delegate's run shows in its parent's trace.
-- An empty statuses array is no filter. COALESCE because a nil Go slice
-- arrives as NULL, and cardinality(NULL) is NULL, which would match nothing.
-- name: ListRunsByTeam :many
SELECT * FROM runs r
WHERE r.team_id = sqlc.arg('team_id')
  AND r.id = r.root_run_id
  AND EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = r.session_id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid))
  AND (COALESCE(cardinality(sqlc.arg('statuses')::text[]), 0) = 0
   OR r.status = ANY(sqlc.arg('statuses')::text[]))
  AND (sqlc.narg('since')::timestamptz IS NULL OR r.created_at >= sqlc.narg('since')::timestamptz)
  AND (NOT sqlc.arg('filter_hub')::bool OR r.hub = sqlc.arg('hub')::bool)
  AND (NOT sqlc.arg('filter_builder')::bool OR r.builder = sqlc.arg('builder')::bool)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (r.created_at, r.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg('page_size');

-- Whom each of these runs handed work to directly: the agents of its
-- children, each once, in the order it first did. A grandchild belongs to
-- the delegate that started it, not to these. Rides runs_root_idx: every
-- child shares its root's root_run_id, and depth 1 keeps only the direct
-- ones of a root.
-- name: ListRunDelegates :many
SELECT root_run_id, agent_name
FROM runs
WHERE root_run_id = ANY(sqlc.arg('run_ids')::uuid[])
  AND depth = 1
GROUP BY root_run_id, agent_name
ORDER BY root_run_id, min(created_at), agent_name;

-- A child is born running and held by its parent's worker: never queued, so
-- the sweep never starts it as a run of its own. Session, root and user come
-- from the parent; the agent is selected within the team, so another team's
-- agent writes nothing.
-- name: StartChildRun :one
INSERT INTO runs (id, team_id, session_id, parent_run_id, root_run_id, parent_tool_call, depth,
                  agent_id, agent_name, user_id, status, input, token_budget,
                  worker, heartbeat_at, started_at)
SELECT sqlc.arg('id')::uuid, p.team_id, p.session_id, p.id, p.root_run_id,
       sqlc.arg('parent_tool_call')::text, p.depth + 1,
       a.id, a.name, p.user_id, 'running', sqlc.arg('input')::text, sqlc.arg('token_budget')::bigint,
       sqlc.arg('worker')::text, now(), now()
FROM runs p
JOIN agents a ON a.id = sqlc.arg('agent_id')::uuid AND a.team_id = p.team_id
WHERE p.id = sqlc.arg('parent_id')::uuid AND p.team_id = sqlc.arg('team_id')::uuid
RETURNING *;

-- What a tree has spent, for the delegate tool's budget check. Elapsed is on
-- the database's clock, from when the root first started, less the time the
-- tree spent waiting for a person: that time is not the task's.
-- name: TreeUsage :one
SELECT count(*)::bigint AS runs,
       coalesce((SELECT extract(epoch FROM now() - root.started_at) - root.waited_seconds
                 FROM runs root WHERE root.id = sqlc.arg('root_id')::uuid), 0)::float8 AS elapsed_seconds
FROM runs r
WHERE r.root_run_id = sqlc.arg('root_id')::uuid;

-- After a crash the resumed parent carries on the children its worker left
-- (AdoptOrphan); whatever is still running beneath it then fails, at any
-- depth, and its model asks again. A child waiting for a person stopped
-- cleanly and is taken up again by the resume, so it is left; a parent that
-- ends takes its waiting children with it (include_waiting).
-- name: ReapChildren :many
WITH RECURSIVE below AS (
    SELECT id FROM runs WHERE parent_run_id = sqlc.arg('parent_id')::uuid
    UNION ALL
    SELECT r.id FROM runs r JOIN below b ON r.parent_run_id = b.id
)
UPDATE runs SET status = 'failed', error = sqlc.arg('reason')::text, finished_at = now(),
                waiting_since = NULL
WHERE id IN (SELECT id FROM below)
  AND (status = 'running' OR (sqlc.arg('include_waiting')::bool AND status = 'waiting'))
RETURNING id;

-- A message starts the run that answers its conversation: the builder's in a
-- builder conversation, the hub's in any other. The message has to
-- be in the run's session: a message from another conversation inserts nothing
-- and the caller gets pgx.ErrNoRows. A run started for steers that arrived too
-- late for the run they were meant for has no message, and passes null.
-- name: CreateChannelRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input,
                  token_budget, hub, builder, message_id, reply_to_channel, transcript)
SELECT sqlc.arg('id')::uuid, t.id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       CASE WHEN ss.builder THEN NULL ELSE a.id END,
       CASE WHEN ss.builder THEN 'builder' ELSE coalesce(a.name, 'orchestrator') END,
       sqlc.narg('user_id')::uuid, sqlc.arg('input')::text,
       CASE WHEN ss.builder THEN NULL ELSE a.token_budget END,
       NOT ss.builder, ss.builder, sqlc.narg('message_id')::uuid,
       -- The dashboard's answer is its stream: there is nothing to post it to.
       ch.provider <> 'dashboard',
       'session:' || sqlc.arg('session_id')::uuid::text
FROM teams t
JOIN sessions ss ON ss.id = sqlc.arg('session_id')::uuid
JOIN channels ch ON ch.id = ss.channel_id
LEFT JOIN team_settings s ON s.team_id = t.id
LEFT JOIN agents a ON a.id = s.hub_agent_id AND a.team_id = t.id
WHERE t.id = sqlc.arg('team_id')::uuid
  AND (sqlc.narg('message_id')::uuid IS NULL OR EXISTS (
        SELECT 1 FROM messages m
        WHERE m.id = sqlc.narg('message_id')::uuid AND m.session_id = sqlc.arg('session_id')::uuid))
RETURNING *;

-- The top-level run working in this conversation now, of any kind: a session
-- has one transcript, so it has one writer. Read it only while holding the
-- session's lock (EnsureSession or LockSession): every path that makes a
-- top-level run terminal takes that lock first, so what this returns cannot
-- finish, and no follow-up can appear, before the caller commits. No run, no
-- row.
-- name: ActiveSessionRun :one
SELECT id FROM runs
WHERE session_id = $1 AND parent_run_id IS NULL AND status IN ('queued', 'running', 'waiting')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- A message for a run that is already working, under the transcript its loop
-- takes steers from before every request: the session's, or a run from before
-- shared transcripts its own id.
-- name: AddSteer :exec
INSERT INTO agent_steers (session, text)
SELECT coalesce(r.transcript, r.id::text), sqlc.arg('text') FROM runs r WHERE r.id = sqlc.arg('run_id')::uuid;

-- Steers written after the run's last request, which nothing will ever read.
-- Deleted as they are returned, oldest first, so they are answered once. Only
-- one top-level run of a session works at a time, so its transcript's steers
-- are its own.
-- name: TakeLeftoverSteers :many
WITH gone AS (
    DELETE FROM agent_steers
    WHERE session = (SELECT coalesce(r.transcript, r.id::text) FROM runs r WHERE r.id = sqlc.arg('run_id')::uuid)
    RETURNING id, text, created_at
)
SELECT text, created_at FROM gone ORDER BY id;

-- A run started for steers that arrived too late for a run that was not a
-- channel's: the same kind of run, the same agent and budget, in the same
-- transcript. A channel's has CreateChannelRun, which also answers.
-- name: CreateFollowUpRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input,
                  token_budget, hub, builder, transcript)
SELECT sqlc.arg('id')::uuid, p.team_id, p.session_id, sqlc.arg('id')::uuid, p.agent_id, p.agent_name,
       p.user_id, sqlc.arg('input')::text, p.token_budget, p.hub, p.builder, p.transcript
FROM runs p
WHERE p.id = sqlc.arg('after')::uuid
RETURNING *;

-- Where a run's first message sits in its transcript, written before the
-- message is. Once only: a resumed run keeps the place it was given.
-- name: SetTranscriptFrom :exec
UPDATE runs SET transcript_from = sqlc.arg('position')::int
WHERE id = sqlc.arg('id')::uuid AND transcript_from IS NULL;

-- What a session's people said since its previous top-level run finished, for
-- the next one: messages that started no run of their own, as a group's are.
-- Newest first, at most turns; with no previous run, the latest. A run's own
-- message is its input, not part of this.
-- name: ListMessagesSinceLastRun :many
SELECT m.text, m.received_at AS at,
       coalesce(nullif(m.sender_name, ''), u.email, '')::text AS author
FROM messages m
LEFT JOIN users u ON u.id = m.user_id
WHERE m.session_id = sqlc.arg('session_id')::uuid
  AND m.received_at < sqlc.arg('before')::timestamptz
  AND NOT m.answer
  AND m.id IS DISTINCT FROM sqlc.narg('own_message')::uuid
  AND m.received_at > coalesce((
        SELECT coalesce(p.finished_at, p.created_at) FROM runs p
        WHERE p.session_id = sqlc.arg('session_id')::uuid AND p.parent_run_id IS NULL
          AND p.id <> sqlc.arg('run_id')::uuid AND p.created_at < sqlc.arg('before')::timestamptz
        ORDER BY p.created_at DESC LIMIT 1), '-infinity'::timestamptz)
ORDER BY m.received_at DESC
LIMIT sqlc.arg('turns')::int;

-- Whether the team's hub can run: a hub agent that still exists, or a model
-- for the built-in orchestrator. The same rule the runtime fails a run on, so
-- a channel never starts a run that is bound to fail with ErrNoModel.
-- The builder runs on the team's model, and only on it.
-- name: BuilderCanRun :one
SELECT EXISTS (
    SELECT 1 FROM team_settings s WHERE s.team_id = $1 AND s.model_name IS NOT NULL
)::bool AS can_run;

-- name: HubCanRun :one
SELECT EXISTS (
    SELECT 1 FROM team_settings s
    LEFT JOIN agents a ON a.id = s.hub_agent_id AND a.team_id = s.team_id
    WHERE s.team_id = $1 AND (a.id IS NOT NULL OR s.model_name IS NOT NULL)
)::bool AS can_run;

-- Whether someone may do something in a team now: their current role there
-- grants it. A sender was bound to a member at approval, and that member can
-- since have been removed or moved to a role without it; this is asked at the
-- moment of acting, so neither starts a run. Permissions are rows, not a role
-- name in code.
-- name: MemberMay :one
SELECT EXISTS (
    SELECT 1 FROM team_members tm
    JOIN role_permissions rp ON rp.role = tm.role
    WHERE tm.team_id = sqlc.arg('team_id') AND tm.user_id = sqlc.arg('user_id')
      AND rp.action = sqlc.arg('action')::text
)::bool AS may;

-- A run a tool suspended is parked: no worker holds it while it waits for a
-- person. Only the worker that holds it parks it.
-- A delegated run that waits for a person stops where it stands, as its
-- root does, holding nothing, with what it said about it as its output for
-- the agent that asked it: the resume takes it up again.
-- name: ParkChild :execrows
UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now(),
                output = sqlc.narg('said')
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running'
  AND parent_run_id IS NOT NULL;

-- A waiting child taken up again by its parent's resume, by this worker:
-- whatever it already did stands, and it carries on from its transcript.
-- name: AdoptChild :one
UPDATE runs SET
    status         = 'running',
    worker         = sqlc.arg('worker')::text,
    heartbeat_at   = now(),
    waited_seconds = waited_seconds + greatest(0, extract(epoch FROM now() - waiting_since))::int,
    waiting_since  = NULL
WHERE id = sqlc.arg('id')::uuid AND status = 'waiting' AND parent_run_id IS NOT NULL
RETURNING *;

-- A run's children whose worker stopped while they ran: running, and not
-- heartbeating for stale_seconds. Only a resumed parent asks, and it carries
-- them on.
-- name: ListOrphanedChildren :many
SELECT * FROM runs
WHERE parent_run_id = sqlc.arg('parent_id')::uuid AND status = 'running'
  AND heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8)
ORDER BY created_at, id;

-- A child whose worker stopped, taken up by its resumed parent's worker. No
-- row: it beat again since it was listed, is no longer running, or is not
-- that parent's.
-- name: AdoptOrphan :one
UPDATE runs SET worker = sqlc.arg('worker')::text, heartbeat_at = now()
WHERE id = sqlc.arg('id')::uuid AND status = 'running' AND parent_run_id = sqlc.arg('parent_id')::uuid
  AND heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8)
RETURNING *;

-- A run's children that wait for a person, in the order they were started.
-- name: ListWaitingChildren :many
SELECT * FROM runs
WHERE parent_run_id = sqlc.arg('parent_id')::uuid AND status = 'waiting'
ORDER BY created_at, id;

-- Whether a run whose turn has ended is waiting rather than finished: a
-- request it made that its model has not been told the answer to, or a child
-- that waits.
-- name: RunAwaits :one
SELECT (EXISTS (SELECT 1 FROM approvals a WHERE a.run_id = sqlc.arg('id')::uuid AND a.told_at IS NULL)
     OR EXISTS (SELECT 1 FROM runs c WHERE c.parent_run_id = sqlc.arg('id')::uuid AND c.status = 'waiting'))::bool AS awaits;

-- name: ParkRun :execrows
UPDATE runs SET status = 'waiting', worker = NULL, heartbeat_at = NULL, waiting_since = now(),
                output = sqlc.narg('said')
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running';

-- A parked run whose tree waits on nothing more is queued again, under its
-- next attempt: a finished DBOS workflow id never runs again, so each resume
-- is a workflow of its own. The time spent waiting is not the run's.
-- name: RequeueAnswered :one
UPDATE runs SET
    status         = 'queued',
    attempt        = attempt + 1,
    waited_seconds = waited_seconds + greatest(0, extract(epoch FROM now() - waiting_since))::int,
    waiting_since  = NULL
WHERE runs.id = $1 AND runs.status = 'waiting'
  AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.root_run_id = runs.id AND a.status = 'pending')
RETURNING runs.id, runs.attempt;

-- The run's session, when nothing else in it is queued or running: the
-- session's workspace may stop. Null for a run with no session, or one whose
-- session is still in use.
--
-- name: IdleSessionOf :one
SELECT r.session_id::uuid AS session_id, r.team_id FROM runs r
WHERE r.id = $1
  AND r.session_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM runs o
      WHERE o.session_id = r.session_id AND o.id <> r.id AND o.status IN ('queued', 'running'));

-- name: SessionIsIdle :one
SELECT NOT EXISTS (
    SELECT 1 FROM runs WHERE session_id = sqlc.arg('session_id')::uuid AND status IN ('queued', 'running')) AS idle;
