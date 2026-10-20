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
-- name: TakeOverStaleRuns :many
UPDATE runs SET status = 'queued', worker = NULL, heartbeat_at = NULL
WHERE id IN (
    SELECT id FROM runs
    WHERE status = 'running'
      AND heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8)
    ORDER BY heartbeat_at
    LIMIT 20
    FOR UPDATE SKIP LOCKED)
RETURNING id;
