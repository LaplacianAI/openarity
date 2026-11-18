-- Asking is idempotent: a gate retried after a crash, or a resumed run asking
-- about the same call again, conflicts on (run_id, call_id) and gets no row,
-- and reads the one it wrote with GetApprovalByCall. DO NOTHING rather than a
-- no-op DO UPDATE, so the retry writes nothing. A pending code that repeats
-- under the same root is a 23505 on approvals_pending_code_key: the caller
-- draws another code.
-- name: InsertApproval :one
INSERT INTO approvals (
    team_id, run_id, root_run_id, call_id, tool, class,
    arguments, summary, code, requested_for, expires_at, require_other
) VALUES (
    sqlc.arg(team_id), sqlc.arg(run_id), sqlc.arg(root_run_id), sqlc.arg(call_id),
    sqlc.arg(tool), sqlc.arg(class), sqlc.arg(arguments), sqlc.arg(summary),
    sqlc.arg(code), sqlc.narg(requested_for), sqlc.arg(expires_at), sqlc.arg(require_other)
)
ON CONFLICT (run_id, call_id) DO NOTHING
RETURNING *;

-- name: GetApprovalByCall :one
SELECT * FROM approvals WHERE run_id = $1 AND call_id = $2;

-- Scoped to the team, so an id from another team is a missing row.
-- name: GetApproval :one
SELECT * FROM approvals WHERE id = $1 AND team_id = $2;

-- Held for the length of a decision, so two people answering at once are
-- served one after the other and the second sees the first's answer.
-- name: LockApproval :one
SELECT * FROM approvals WHERE id = $1 AND team_id = $2 FOR UPDATE;

-- Only a pending request can be decided; anything else matches nothing, so a
-- late answer to an expired or already-answered request changes nothing.
-- name: DecideApproval :one
UPDATE approvals SET
    status         = sqlc.arg(status),
    decided_by     = sqlc.narg(decided_by),
    decided_via    = sqlc.arg(decided_via),
    decision_scope = sqlc.narg(decision_scope),
    reason         = sqlc.narg(reason),
    decided_at     = now()
WHERE id = sqlc.arg(id) AND team_id = sqlc.arg(team_id) AND status = 'pending'
RETURNING *;

-- An approval is spent by the call it was for, once. The condition is on the
-- row being updated, so two gates racing for one approval are serialised by
-- its row lock and the second matches nothing.
-- name: ClaimApprovalUse :one
UPDATE approvals SET used_at = now()
WHERE id = $1 AND status = 'approved' AND used_at IS NULL
RETURNING *;

-- name: ListApprovals :many
SELECT * FROM approvals a
WHERE a.team_id = sqlc.arg('team_id')
  AND (COALESCE(cardinality(sqlc.arg('statuses')::text[]), 0) = 0
   OR a.status = ANY(sqlc.arg('statuses')::text[]))
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (a.created_at, a.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg('page_size');

-- What a conversation's runs are waiting on, oldest first, for a reply in
-- the chat to be matched against by its code.
-- name: ListPendingApprovalsBySession :many
SELECT a.* FROM approvals a
JOIN runs r ON r.id = a.root_run_id
WHERE r.session_id = $1 AND a.status = 'pending'
ORDER BY a.created_at, a.id;

-- One sweep's worth of requests past their time. SKIP LOCKED so a request
-- someone is deciding right now is left to them, and two sweeps never take
-- the same row.
-- name: ExpireDueApprovals :many
WITH due AS (
    SELECT id FROM approvals
    WHERE status = 'pending' AND expires_at <= now()
    ORDER BY expires_at
    LIMIT sqlc.arg('batch')
    FOR UPDATE SKIP LOCKED
)
UPDATE approvals a SET status = 'expired', decided_via = 'sweep', decided_at = now()
FROM due WHERE a.id = due.id
RETURNING a.*;

-- A cancelled run's requests are no longer anyone's to answer.
-- name: CancelRunApprovals :many
UPDATE approvals SET status = 'cancelled', decided_via = 'sweep', decided_at = now()
WHERE root_run_id = $1 AND status = 'pending'
RETURNING *;

-- An approval for the rest of the run: this tool, any arguments, granted on
-- an earlier call of the same run tree.
-- name: FindRunScopedApproval :one
SELECT * FROM approvals
WHERE root_run_id = $1 AND tool = $2 AND status = 'approved' AND decision_scope = 'run'
ORDER BY decided_at
LIMIT 1;

-- Every request a run made, for a resume to tell which of its open calls a
-- decision is waiting to be acted on.
-- name: ListRunApprovals :many
SELECT * FROM approvals WHERE run_id = $1 ORDER BY created_at, id;

-- Whether anything of a run tree still waits for a person.
-- name: CountPendingApprovals :one
SELECT count(*) FROM approvals WHERE root_run_id = $1 AND status = 'pending';
