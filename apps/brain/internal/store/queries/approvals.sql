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

-- A team's requests, newest first, as a person may see them: only those
-- whose run they may read, by the rule GetRunVisible keeps (a direct
-- conversation's are its person's, and a moderator's). A request's summary
-- and arguments quote the call, so they reach no further than the run does.
-- Filtered by status, and by the run tree they were asked under.
-- name: ListVisibleApprovals :many
SELECT a.* FROM approvals a
JOIN runs r ON r.id = a.root_run_id
JOIN sessions s ON s.id = r.session_id
WHERE a.team_id = sqlc.arg('team_id')
  AND (s.kind <> 'direct' OR sqlc.arg('moderator')::bool OR s.user_id = sqlc.arg('viewer')::uuid)
  AND (COALESCE(cardinality(sqlc.arg('statuses')::text[]), 0) = 0
   OR a.status = ANY(sqlc.arg('statuses')::text[]))
  AND (sqlc.narg('root_run_id')::uuid IS NULL OR a.root_run_id = sqlc.narg('root_run_id')::uuid)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (a.created_at, a.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg('page_size');

-- One request, as ListVisibleApprovals would show it; another team's, or
-- one whose run the person may not read, is no row.
-- name: GetVisibleApproval :one
SELECT a.* FROM approvals a
JOIN runs r ON r.id = a.root_run_id
JOIN sessions s ON s.id = r.session_id
WHERE a.id = sqlc.arg('id') AND a.team_id = sqlc.arg('team_id')
  AND (s.kind <> 'direct' OR sqlc.arg('moderator')::bool OR s.user_id = sqlc.arg('viewer')::uuid);

-- What a conversation's runs are waiting on, oldest first, for a reply in
-- the chat to be matched against by its code.
-- name: ListPendingApprovalsBySession :many
SELECT a.* FROM approvals a
JOIN runs r ON r.id = a.root_run_id
WHERE r.session_id = $1 AND a.status = 'pending'
ORDER BY a.created_at, a.id;

-- One sweep's worth of requests past their time, read without a lock: each
-- is then expired the way a late answer expires it, the session taken first
-- and the request after, so the sweep and a person deciding take the two
-- locks in the same order and never deadlock.
-- name: ListDueApprovals :many
SELECT id, team_id FROM approvals
WHERE status = 'pending' AND expires_at <= now()
ORDER BY expires_at
LIMIT sqlc.arg('batch');

-- A run that ended, however it ended, leaves nothing to answer.
-- name: CancelRunApprovals :many
UPDATE approvals SET status = 'cancelled', decided_via = 'sweep', decided_at = now()
WHERE root_run_id = $1 AND status = 'pending'
RETURNING *;

-- A person who writes again before answering has moved on from what they
-- were asked: their own requests in the tree end, answered by them, and the
-- run reads what they wrote instead. Other people's requests stand.
-- name: SupersedeApprovals :many
UPDATE approvals SET status = 'cancelled', decided_by = sqlc.arg('user_id')::uuid,
                     decided_via = sqlc.arg('via')::text, decided_at = now()
WHERE root_run_id = sqlc.arg('root_run_id')::uuid AND status = 'pending'
  AND requested_for = sqlc.arg('user_id')::uuid
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

-- A call asked for again while it waits, in the same run and with the same
-- arguments, is the request already made: asked once, answered once.
-- name: FindPendingRequest :one
SELECT * FROM approvals
WHERE run_id = sqlc.arg('run_id') AND tool = sqlc.arg('tool') AND status = 'pending'
  AND arguments = sqlc.arg('arguments')::jsonb
LIMIT 1;

-- What a run's requests came to that its model has not yet been told, in the
-- order they were made: a resume acts on each, then tells it.
-- name: ListUntoldApprovals :many
SELECT * FROM approvals
WHERE run_id = $1 AND status <> 'pending' AND told_at IS NULL
ORDER BY created_at, id;

-- name: MarkApprovalsTold :exec
UPDATE approvals SET told_at = now()
WHERE id = ANY(sqlc.arg('ids')::uuid[]) AND status <> 'pending' AND told_at IS NULL;

-- What a tree still asks, for the prompt its conversation is sent when it
-- parks: every request the brain has not ended, oldest first.
-- name: ListTreePending :many
SELECT * FROM approvals
WHERE root_run_id = $1 AND status = 'pending'
ORDER BY created_at, id;
