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
SELECT id FROM teams ORDER BY id;
