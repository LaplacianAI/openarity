-- Watchers, the pages and feeds they poll, and what each check did. See
-- internal/triggers.

-- name: CreateWatcher :one
INSERT INTO watchers (
    id, team_id, created_by, agent_id, hub, name, objective, source, target_id, selector,
    every_seconds, cooldown_seconds, status, next_check_at, proposed_run, proposed_in, target, target_label
) VALUES (
    sqlc.arg('id'), sqlc.arg('team_id'), sqlc.arg('created_by')::uuid, sqlc.narg('agent_id'), sqlc.arg('hub'),
    sqlc.arg('name'), sqlc.arg('objective'), sqlc.arg('source'), sqlc.narg('target_id'), sqlc.narg('selector'),
    sqlc.narg('every_seconds'), sqlc.arg('cooldown_seconds'), sqlc.arg('status'), sqlc.narg('next_check_at'),
    sqlc.narg('proposed_run'), sqlc.narg('proposed_in'), sqlc.arg('target'), sqlc.arg('target_label')
)
RETURNING *;

-- The page or feed a watcher polls, shared by every watcher of the same
-- address. One already there is checked by the soonest of them.
-- name: UpsertWatchTarget :one
INSERT INTO watch_targets (id, kind, url, host, next_check_at)
VALUES (sqlc.arg('id'), sqlc.arg('kind'), sqlc.arg('url'), sqlc.arg('host'), sqlc.arg('next_check_at'))
ON CONFLICT (kind, url) DO UPDATE
    SET next_check_at = least(watch_targets.next_check_at, excluded.next_check_at)
RETURNING *;

-- A target is checked no later than at: a watcher made, resumed or asked to
-- check now.
-- name: PullTargetCheck :exec
UPDATE watch_targets SET next_check_at = least(next_check_at, sqlc.arg('at')::timestamptz)
WHERE id = sqlc.arg('id');

-- name: GetWatchTarget :one
SELECT * FROM watch_targets WHERE id = sqlc.arg('id');

-- name: LockWatcher :one
SELECT * FROM watchers
WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id') AND status <> 'deleted'
FOR UPDATE;

-- One of a maker's own, by name: how a person or an agent picks one.
-- name: WatcherIDByName :one
SELECT id FROM watchers
WHERE team_id = sqlc.arg('team_id') AND created_by = sqlc.arg('maker')::uuid
  AND lower(name) = lower(sqlc.arg('name')) AND status <> 'deleted';

-- Every column a change or a check may move, written back whole after the
-- caller locked the row and worked out what it becomes.
-- name: SaveWatcher :one
UPDATE watchers SET
    created_by       = sqlc.narg('created_by'),
    agent_id         = sqlc.narg('agent_id'),
    hub              = sqlc.arg('hub'),
    name             = sqlc.arg('name'),
    objective        = sqlc.arg('objective'),
    target_id        = sqlc.narg('target_id'),
    selector         = sqlc.narg('selector'),
    every_seconds    = sqlc.narg('every_seconds'),
    cooldown_seconds = sqlc.arg('cooldown_seconds'),
    state            = sqlc.arg('state'),
    status           = sqlc.arg('status'),
    paused_reason    = sqlc.narg('paused_reason'),
    deleted_at       = sqlc.narg('deleted_at'),
    next_check_at    = sqlc.narg('next_check_at'),
    last_run_at      = sqlc.narg('last_run_at'),
    consecutive_failures = sqlc.arg('consecutive_failures'),
    updated_at       = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- A watcher as it is shown: with its conversation, the address it polls, and
-- what its newest check did.
-- name: GetWatcherView :one
SELECT sqlc.embed(w), ss.id AS session_id, ss.channel_id AS session_channel_id,
       coalesce(t.url, '')::text AS url,
       coalesce(lc.outcome, '')::text AS last_outcome, lc.checked_at AS last_checked_at
FROM watchers w
JOIN sessions ss ON ss.watcher_id = w.id
LEFT JOIN watch_targets t ON t.id = w.target_id
LEFT JOIN watcher_checks lc ON lc.id = (
    SELECT c.id FROM watcher_checks c WHERE c.watcher_id = w.id ORDER BY c.checked_at DESC, c.id DESC LIMIT 1)
WHERE w.id = sqlc.arg('id') AND w.team_id = sqlc.arg('team_id');

-- A team's watchers, newest first, a page at a time: the viewer's own unless
-- everyone's is asked for by one who may see them; deleted ones only when
-- asked for by status.
-- name: ListWatcherViews :many
SELECT sqlc.embed(w), ss.id AS session_id, ss.channel_id AS session_channel_id,
       coalesce(t.url, '')::text AS url,
       coalesce(lc.outcome, '')::text AS last_outcome, lc.checked_at AS last_checked_at
FROM watchers w
JOIN sessions ss ON ss.watcher_id = w.id
LEFT JOIN watch_targets t ON t.id = w.target_id
LEFT JOIN watcher_checks lc ON lc.id = (
    SELECT c.id FROM watcher_checks c WHERE c.watcher_id = w.id ORDER BY c.checked_at DESC, c.id DESC LIMIT 1)
WHERE w.team_id = sqlc.arg('team_id')
  AND (sqlc.arg('everyone')::bool OR w.created_by = sqlc.arg('viewer')::uuid)
  AND (CASE WHEN cardinality(sqlc.arg('statuses')::text[]) = 0 THEN w.status <> 'deleted'
            ELSE w.status = ANY(sqlc.arg('statuses')::text[]) END)
  AND (sqlc.narg('after_created')::timestamptz IS NULL
       OR (w.created_at, w.id) < (sqlc.narg('after_created')::timestamptz, sqlc.narg('after_id')::uuid))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('page_size');

-- name: GetTriggerSessionOfWatcher :one
SELECT * FROM sessions WHERE watcher_id = $1;

-- A watcher's checks, newest first, a page at a time.
-- name: ListWatcherChecks :many
SELECT * FROM watcher_checks
WHERE watcher_id = sqlc.arg('watcher_id') AND team_id = sqlc.arg('team_id')
  AND (sqlc.narg('before')::timestamptz IS NULL OR checked_at < sqlc.narg('before')::timestamptz)
ORDER BY checked_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- name: InsertWatcherCheck :one
INSERT INTO watcher_checks (watcher_id, team_id, checked_at, outcome, detail, run_id)
VALUES (sqlc.arg('watcher_id'), sqlc.arg('team_id'), sqlc.arg('checked_at'), sqlc.arg('outcome'),
        sqlc.narg('detail'), sqlc.narg('run_id'))
RETURNING id;

-- How many targets are due: what the tick fans out by.
-- name: CountDueTargets :one
SELECT count(*)::int FROM watch_targets WHERE next_check_at <= sqlc.arg('now')::timestamptz;

-- Due targets, taken: next_check_at moves on to a lease, so a check that
-- crashed is tried again then, and finishing sets the real time.
-- name: ClaimDueTargets :many
UPDATE watch_targets SET next_check_at = sqlc.arg('lease')::timestamptz
WHERE id IN (
    SELECT id FROM watch_targets
    WHERE next_check_at <= sqlc.arg('now')::timestamptz
    ORDER BY next_check_at
    LIMIT sqlc.arg('page_size')
    FOR UPDATE SKIP LOCKED)
RETURNING *;

-- A host taken for one batch, unless another batch holds it or it asked us
-- to wait. Its row is made on first use. No row back is a host not free.
-- name: TakeHost :one
INSERT INTO watch_hosts (host, busy_until) VALUES (sqlc.arg('host'), sqlc.arg('until')::timestamptz)
ON CONFLICT (host) DO UPDATE SET busy_until = excluded.busy_until
WHERE watch_hosts.busy_until < sqlc.arg('now')::timestamptz
  AND watch_hosts.backoff_until < sqlc.arg('now')::timestamptz
RETURNING host;

-- name: ReleaseHost :exec
UPDATE watch_hosts SET busy_until = '-infinity' WHERE host = sqlc.arg('host');

-- A host that answered 429 or 503 is left alone until it said, or a default.
-- name: BackOffHost :exec
UPDATE watch_hosts SET backoff_until = greatest(backoff_until, sqlc.arg('until')::timestamptz), busy_until = '-infinity'
WHERE host = sqlc.arg('host');

-- name: DeferTarget :exec
UPDATE watch_targets SET next_check_at = sqlc.arg('at')::timestamptz WHERE id = sqlc.arg('id');

-- A target read: its validators kept for the next conditional request, and
-- its errors forgotten.
-- name: TargetFetched :exec
UPDATE watch_targets SET etag = sqlc.narg('etag'), last_modified = sqlc.narg('last_modified'),
    consecutive_errors = 0, failing_since = NULL
WHERE id = sqlc.arg('id');

-- A target that could not be read: counted, and since when.
-- name: TargetFailed :one
UPDATE watch_targets SET consecutive_errors = consecutive_errors + 1,
    failing_since = coalesce(failing_since, sqlc.arg('now')::timestamptz),
    next_check_at = sqlc.arg('next_check_at')::timestamptz
WHERE id = sqlc.arg('id')
RETURNING consecutive_errors, failing_since;

-- The active watchers of a target due by then: what one fetch is checked
-- for. fresh is one that has seen nothing yet, whose look must read the
-- body, so the fetch is not conditional.
-- name: DueWatchersOfTarget :many
SELECT id, team_id,
       ((state->>'hash') IS NULL AND jsonb_typeof(state->'seen') IS DISTINCT FROM 'array')::bool AS fresh
FROM watchers
WHERE target_id = sqlc.arg('target_id') AND status = 'active'
  AND next_check_at <= sqlc.arg('due_by')::timestamptz
ORDER BY next_check_at, id;

-- A target is next checked when its soonest active watcher is due; with none
-- active it is not checked until one is.
-- name: SetTargetNextCheck :exec
UPDATE watch_targets t SET next_check_at = coalesce(
    (SELECT min(w.next_check_at) FROM watchers w WHERE w.target_id = t.id AND w.status = 'active'),
    'infinity')
WHERE t.id = sqlc.arg('id');

-- How a run a watcher started ended, on its check, once.
-- name: SetCheckOutcome :one
UPDATE watcher_checks SET outcome = sqlc.arg('outcome')
WHERE run_id = sqlc.arg('run_id') AND outcome IN ('changed', 'delivered')
RETURNING watcher_id;

-- A watcher's run's end counted: a success clears the failures, a failure
-- adds one and the fifth in a row pauses one still active. A cancelled run is
-- neither.
-- name: CountWatcherRun :one
UPDATE watchers SET
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
    next_check_at = CASE
        WHEN sqlc.arg('failed')::bool AND status = 'active'
             AND consecutive_failures + 1 >= sqlc.arg('pause_after')::int THEN NULL
        ELSE next_check_at END,
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING status, paused_reason;

-- What a maker has not yet seen of their watchers: what went wrong, and what
-- was answered in a conversation of its own. A silent run tells nobody; a
-- quiet check is not news.
-- name: ListWatcherNews :many
SELECT c.watcher_id, w.name, ss.id AS session_id, c.checked_at, c.outcome, c.run_id
FROM watcher_checks c
JOIN watchers w ON w.id = c.watcher_id
JOIN sessions ss ON ss.watcher_id = w.id
JOIN channels ch ON ch.id = ss.channel_id
WHERE w.team_id = sqlc.arg('team_id') AND w.created_by = sqlc.arg('maker')::uuid
  AND w.status <> 'deleted' AND c.seen_at IS NULL
  AND (c.outcome IN ('failed', 'error', 'skipped_cap', 'skipped_maker', 'skipped_target')
       OR (c.outcome = 'answered' AND ch.provider = 'dashboard'))
ORDER BY c.checked_at DESC, c.id DESC
LIMIT sqlc.arg('page_size');

-- name: MarkWatcherSeen :execrows
UPDATE watcher_checks c SET seen_at = now()
FROM watchers w
WHERE w.id = c.watcher_id AND c.watcher_id = sqlc.arg('watcher_id')
  AND w.team_id = sqlc.arg('team_id') AND w.created_by = sqlc.arg('maker')::uuid
  AND c.seen_at IS NULL;

-- A target failing too long pauses its active watchers.
-- name: PauseFailingWatchers :many
UPDATE watchers SET status = 'paused', paused_reason = 'failing', next_check_at = NULL, updated_at = now()
WHERE target_id = sqlc.arg('target_id') AND status = 'active'
RETURNING id, team_id;

-- Proposals nobody confirmed are deleted as any watcher is: marked so, its
-- target let go, its conversation closed and kept.
-- name: ExpireProposedWatchers :execrows
WITH gone AS (
    UPDATE watchers SET status = 'deleted', deleted_at = now(), next_check_at = NULL,
        target_id = NULL, updated_at = now()
    WHERE (status = 'proposed' AND created_at < sqlc.arg('proposed_before'))
       OR (status = 'waiting_secret' AND created_at < sqlc.arg('waiting_before'))
    RETURNING id
)
UPDATE sessions SET status = 'closed'
WHERE watcher_id IN (SELECT id FROM gone);

-- Each watcher keeps its newest checks; the rest go a batch at a time.
-- name: PruneWatcherChecks :execrows
DELETE FROM watcher_checks c
USING (
    SELECT id FROM (
        SELECT id, row_number() OVER (PARTITION BY watcher_id ORDER BY checked_at DESC, id DESC) AS n
        FROM watcher_checks
    ) ranked
    WHERE n > sqlc.arg('keep')::int
    LIMIT sqlc.arg('batch')
) old
WHERE c.id = old.id;

-- Targets no watcher polls any more.
-- name: DeleteUnwatchedTargets :execrows
DELETE FROM watch_targets t
WHERE NOT EXISTS (SELECT 1 FROM watchers w WHERE w.target_id = t.id);

-- A watcher taken over by an admin: its own dashboard conversation is with
-- them from now on.
-- name: SetWatcherSessionUser :exec
UPDATE sessions SET user_id = sqlc.arg('user_id')::uuid
WHERE watcher_id = sqlc.arg('watcher_id')::uuid AND kind = 'direct'
  AND channel_id = (SELECT id FROM channels WHERE team_id = sessions.team_id AND provider = 'dashboard');
