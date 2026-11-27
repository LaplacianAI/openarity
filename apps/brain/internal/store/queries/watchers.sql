-- Watchers, the pages and feeds they poll, and what each check did. See
-- internal/triggers.

-- name: CreateWatcher :one
INSERT INTO watchers (
    id, team_id, created_by, agent_id, hub, name, objective, source, target_id, selector, hook_id,
    every_seconds, cooldown_seconds, status, next_check_at, proposed_run, proposed_in, target, target_label
) VALUES (
    sqlc.arg('id'), sqlc.arg('team_id'), sqlc.arg('created_by')::uuid, sqlc.narg('agent_id'), sqlc.arg('hub'),
    sqlc.arg('name'), sqlc.arg('objective'), sqlc.arg('source'), sqlc.narg('target_id'), sqlc.narg('selector'),
    sqlc.narg('hook_id'), sqlc.narg('every_seconds'), sqlc.arg('cooldown_seconds'), sqlc.arg('status'),
    sqlc.narg('next_check_at'), sqlc.narg('proposed_run'), sqlc.narg('proposed_in'), sqlc.arg('target'),
    sqlc.arg('target_label')
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

-- A host's hold renewed by the batch that holds it, before each fetch. No row
-- is a hold lost: another batch took the host once the hold lapsed.
-- name: RenewHost :execrows
UPDATE watch_hosts SET busy_until = sqlc.arg('until')::timestamptz
WHERE host = sqlc.arg('host') AND busy_until = sqlc.arg('held')::timestamptz;

-- A host let go by the batch that holds it, and only by it.
-- name: ReleaseHost :exec
UPDATE watch_hosts SET busy_until = '-infinity'
WHERE host = sqlc.arg('host') AND busy_until = sqlc.arg('held')::timestamptz;

-- A host that answered 429 or 503 is left alone until it said, or a default,
-- whoever holds it; the batch that held it lets it go.
-- name: BackOffHost :exec
UPDATE watch_hosts SET backoff_until = greatest(backoff_until, sqlc.arg('until')::timestamptz),
    busy_until = CASE WHEN busy_until = sqlc.arg('held')::timestamptz THEN '-infinity' ELSE busy_until END
WHERE host = sqlc.arg('host');

-- When a host not free now is free again: another batch's hold, or its wait.
-- name: HostFreeAt :one
SELECT greatest(busy_until, backoff_until)::timestamptz FROM watch_hosts WHERE host = sqlc.arg('host');

-- name: DeferTarget :exec
UPDATE watch_targets SET next_check_at = sqlc.arg('at')::timestamptz WHERE id = sqlc.arg('id');

-- A target read: its validators kept for the next conditional request, or
-- none when a watcher could not take in what was read, so its next look reads
-- it whole again; and its errors forgotten.
-- name: TargetFetched :exec
UPDATE watch_targets SET etag = sqlc.narg('etag'), last_modified = sqlc.narg('last_modified'),
    consecutive_errors = 0, failing_since = NULL
WHERE id = sqlc.arg('id');

-- A target that answered not modified: its errors forgotten, its validators
-- left as they are, so one forgotten meanwhile stays forgotten.
-- name: TargetAnswered :exec
UPDATE watch_targets SET consecutive_errors = 0, failing_since = NULL
WHERE id = sqlc.arg('id');

-- A target that could not be read: counted, and since when.
-- name: TargetFailed :one
UPDATE watch_targets SET consecutive_errors = consecutive_errors + 1,
    failing_since = coalesce(failing_since, sqlc.arg('now')::timestamptz),
    next_check_at = sqlc.arg('next_check_at')::timestamptz
WHERE id = sqlc.arg('id')
RETURNING consecutive_errors, failing_since;

-- The active watchers of a target: every one is compared with what a fetch
-- reads, due or not, as the fetch moves the target's validators for all of
-- them. due is one whose own time has come; fresh one that has seen nothing
-- yet, whose look must read the body, so the fetch is not conditional. fresh
-- reads the keys of watch.State: hash for a page, seen for a feed.
-- name: ActiveWatchersOfTarget :many
SELECT id, team_id, (next_check_at <= sqlc.arg('due_by')::timestamptz)::bool AS due,
       ((state->>'hash') IS NULL AND jsonb_typeof(state->'seen') IS DISTINCT FROM 'array')::bool AS fresh
FROM watchers
WHERE target_id = sqlc.arg('target_id') AND status = 'active'
ORDER BY next_check_at, id;

-- A target read whole at its next look: a watcher resumed may have missed
-- what another's fetches saw while it was paused.
-- name: ForgetTargetValidators :exec
UPDATE watch_targets SET etag = NULL, last_modified = NULL WHERE id = sqlc.arg('id');

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

-- A webhook watcher by the id in its URL. A deleted one has no URL.
-- name: WatcherByHook :one
SELECT * FROM watchers WHERE hook_id = sqlc.arg('hook_id') AND status <> 'deleted';

-- A delivery remembered for a day, so one sent twice starts nothing twice.
-- No row is a delivery already had.
-- name: RememberDelivery :execrows
INSERT INTO watcher_deliveries (watcher_id, key, received_at)
VALUES (sqlc.arg('watcher_id'), sqlc.arg('key'), sqlc.arg('received_at'))
ON CONFLICT (watcher_id, key) DO NOTHING;

-- A delivery kept for the next run that may start.
-- name: HoldDelivery :exec
UPDATE watcher_deliveries SET held = true, body = sqlc.arg('body'), event = sqlc.narg('event')
WHERE watcher_id = sqlc.arg('watcher_id') AND key = sqlc.arg('key');

-- What a webhook watcher holds that one run can take in, oldest first: the
-- first always, then each while the bodies fit the budget. total is how many
-- it holds in all.
-- name: HeldDeliveries :many
SELECT h.key, h.received_at, h.body, h.event, h.total::bigint AS total
FROM (
    SELECT d.key, d.received_at, d.body, d.event,
           sum(octet_length(d.body)) OVER w AS upto, row_number() OVER w AS n, count(*) OVER () AS total
    FROM watcher_deliveries d
    WHERE d.watcher_id = sqlc.arg('watcher_id') AND d.held
    WINDOW w AS (ORDER BY d.received_at, d.key)
) h
WHERE h.n = 1 OR h.upto <= sqlc.arg('budget')::bigint
ORDER BY h.received_at, h.key;

-- Held deliveries a run took in, let go; each is still remembered for its day.
-- name: LetGoOfTakenDeliveries :exec
UPDATE watcher_deliveries SET held = false, body = NULL, event = NULL
WHERE watcher_id = sqlc.arg('watcher_id') AND held AND key = ANY(sqlc.arg('keys')::text[]);

-- Every held delivery let go: dropped by a resume or a delete.
-- name: LetGoOfHeldDeliveries :exec
UPDATE watcher_deliveries SET held = false, body = NULL, event = NULL
WHERE watcher_id = sqlc.arg('watcher_id') AND held;

-- A watcher holds at most keep deliveries: the oldest beyond them are let go.
-- name: TrimHeldDeliveries :execrows
UPDATE watcher_deliveries t SET held = false, body = NULL, event = NULL
WHERE t.watcher_id = sqlc.arg('watcher_id') AND t.held AND t.key IN (
    SELECT d.key FROM watcher_deliveries d
    WHERE d.watcher_id = t.watcher_id AND d.held
    ORDER BY d.received_at DESC, d.key DESC
    OFFSET sqlc.arg('keep')::int);

-- Active webhook watchers holding deliveries whose cooldown has passed, a
-- page at a time after the last one seen.
-- name: WatchersReadyToRelease :many
SELECT w.id, w.team_id FROM watchers w
WHERE w.source = 'webhook' AND w.status = 'active' AND w.id > sqlc.arg('after')::uuid
  AND (w.last_run_at IS NULL OR w.last_run_at + make_interval(secs => w.cooldown_seconds) <= sqlc.arg('now')::timestamptz)
  AND EXISTS (SELECT 1 FROM watcher_deliveries d WHERE d.watcher_id = w.id AND d.held)
ORDER BY w.id
LIMIT sqlc.arg('page_size');

-- Deliveries remembered for their day and no longer held.
-- name: ForgetOldDeliveries :execrows
DELETE FROM watcher_deliveries WHERE NOT held AND received_at < sqlc.arg('before');
