-- A claim is a committed mark, not a held lock: the projector embeds and
-- writes FalkorDB between claiming and forgetting, and a transaction held open
-- across a network call is a connection and a lock nobody else can have.
-- SKIP LOCKED only keeps two workers from marking the same row at once.
--
-- name: ClaimGraphOutbox :many
UPDATE graph_outbox
SET attempts = attempts + 1, last_attempt_at = now()
WHERE id IN (
    SELECT o.id FROM graph_outbox o
    WHERE o.last_attempt_at IS NULL OR o.last_attempt_at < sqlc.arg('retry_before')
    ORDER BY o.last_attempt_at NULLS FIRST, o.id
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- Only the ids that were claimed and projected. A row written for the same
-- entity after the claim has an id of its own and stays, so a change made
-- while the projector was reading is projected again rather than lost.
--
-- name: ForgetGraphOutbox :exec
DELETE FROM graph_outbox WHERE id = ANY(sqlc.arg('ids')::bigint[]);

-- The same shape as the tombstones' backlog, for the same reason: one row
-- when anything is outstanding, none when nothing is.
--
-- name: GraphOutboxBacklog :many
SELECT count(*) OVER () AS outstanding, created_at AS oldest
FROM graph_outbox
ORDER BY created_at
LIMIT 1;

-- name: ListTeamIDs :many
SELECT id FROM teams WHERE deleted_at IS NULL ORDER BY id;

-- A session lock, taken and released on one pinned connection by
-- Store.WithAdvisoryLock. Session rather than transaction: the work it guards
-- crosses network calls, and a transaction held open that long pins a
-- snapshot.
--
-- name: TryAdvisoryLock :one
SELECT pg_try_advisory_lock(sqlc.arg('key')::bigint) AS acquired;

-- name: AdvisoryUnlock :one
SELECT pg_advisory_unlock(sqlc.arg('key')::bigint) AS released;

-- Everything of a team that has a node in the graph, named as the outbox
-- names it, so a rebuild feeds the projector exactly what a drain would.
--
-- name: ListGraphEntities :many
SELECT 'mcp_server'::text AS entity, id FROM mcp_servers WHERE mcp_servers.team_id = sqlc.arg('team_id')
UNION ALL
SELECT 'skill', id FROM skills WHERE skills.team_id = sqlc.arg('team_id')
UNION ALL
SELECT 'mcp_tool', id FROM mcp_tools WHERE mcp_tools.team_id = sqlc.arg('team_id')
UNION ALL
SELECT 'agent', id FROM agents WHERE agents.team_id = sqlc.arg('team_id');
