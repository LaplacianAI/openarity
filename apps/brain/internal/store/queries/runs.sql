-- The agent's team is checked by the SELECT: an agent of another team inserts
-- nothing, and the caller gets pgx.ErrNoRows.
-- name: CreateRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_id, agent_name, user_id, input, token_budget)
SELECT sqlc.arg('id')::uuid, a.team_id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       a.id, a.name, sqlc.narg('user_id')::uuid, sqlc.arg('input')::text, a.token_budget
FROM agents a
WHERE a.id = sqlc.arg('agent_id') AND a.team_id = sqlc.arg('team_id')
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
        + make_interval(secs => sqlc.arg('wall_clock_seconds')::float8) - now())::float8
        AS remaining_seconds;

-- No row means this worker no longer holds the run.
-- name: HeartbeatRun :one
UPDATE runs SET heartbeat_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running'
RETURNING cancel_requested_at;

-- Counted as it is spent, so a resumed run carries on from the same total.
-- name: AddRunUsage :one
UPDATE runs SET
    input_tokens  = input_tokens  + sqlc.arg('input_tokens')::bigint,
    output_tokens = output_tokens + sqlc.arg('output_tokens')::bigint
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running'
RETURNING (input_tokens + output_tokens)::bigint AS spent;

-- name: FinishRun :execrows
UPDATE runs SET
    status      = sqlc.arg('status')::text,
    output      = sqlc.narg('output'),
    error       = sqlc.narg('error'),
    finished_at = now()
WHERE id = sqlc.arg('id') AND worker = sqlc.arg('worker')::text AND status = 'running';

-- Queued: cancelled now, since no worker holds it. Running: flagged for the
-- worker's next heartbeat. Finished: no row, which the handler answers 409.
-- name: RequestRunCancel :one
UPDATE runs SET
    cancel_requested_at = coalesce(cancel_requested_at, now()),
    status      = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
    finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END
WHERE id = sqlc.arg('id') AND status IN ('queued', 'running')
RETURNING *;

-- name: ListUnstartedRuns :many
SELECT id FROM runs
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
       a.id, a.name, p.user_id, 'running', sqlc.arg('input')::text, a.token_budget,
       sqlc.arg('worker')::text, now(), now()
FROM runs p
JOIN agents a ON a.id = sqlc.arg('agent_id')::uuid AND a.team_id = p.team_id
WHERE p.id = sqlc.arg('parent_id')::uuid AND p.team_id = sqlc.arg('team_id')::uuid
RETURNING *;

-- What a tree has spent, for the delegate tool's budget check. Elapsed is on
-- the database's clock, from when the root first started.
-- name: TreeUsage :one
SELECT coalesce(sum(r.input_tokens + r.output_tokens), 0)::bigint AS tokens,
       count(*)::bigint AS runs,
       coalesce(extract(epoch FROM now() - (
           SELECT started_at FROM runs WHERE id = sqlc.arg('root_id')::uuid)), 0)::float8 AS elapsed_seconds
FROM runs r
WHERE r.root_run_id = sqlc.arg('root_id')::uuid;

-- After a crash the resumed parent asks again rather than finding what it
-- asked before, so whatever was still running beneath it fails, at any depth.
-- name: ReapChildren :many
WITH RECURSIVE below AS (
    SELECT id FROM runs WHERE parent_run_id = sqlc.arg('parent_id')::uuid
    UNION ALL
    SELECT r.id FROM runs r JOIN below b ON r.parent_run_id = b.id
)
UPDATE runs SET status = 'failed', error = sqlc.arg('reason')::text, finished_at = now()
WHERE id IN (SELECT id FROM below) AND status = 'running'
RETURNING id;
