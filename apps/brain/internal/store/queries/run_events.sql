-- Written only by the worker holding the run: HAVING drops the row when it
-- does not, so a worker that lost its run cannot interleave with the one that
-- has it. The aggregate always yields one row, so HAVING is what decides.
-- name: AppendRunEvent :one
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT sqlc.arg('run_id')::uuid, coalesce(max(e.seq), 0) + 1,
       sqlc.arg('kind')::text, sqlc.arg('payload')::jsonb
FROM run_events e
WHERE e.run_id = sqlc.arg('run_id')::uuid
HAVING EXISTS (
    SELECT 1 FROM runs
    WHERE id = sqlc.arg('run_id')::uuid AND worker = sqlc.arg('worker')::text)
RETURNING seq;

-- One of the brain's own events on a run no worker is writing to, or one a
-- worker is: the caller holds the run's session (LockRunSession), which every
-- writer of a run's events takes, so the next seq is the caller's alone.
-- name: AppendBrainRunEvent :one
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT sqlc.arg('run_id')::uuid, coalesce(max(e.seq), 0) + 1,
       sqlc.arg('kind')::text, sqlc.arg('payload')::jsonb
FROM run_events e
WHERE e.run_id = sqlc.arg('run_id')::uuid
RETURNING seq;

-- name: ListRunEvents :many
SELECT * FROM run_events
WHERE run_id = sqlc.arg('run_id') AND seq > sqlc.arg('after')::int
ORDER BY seq
LIMIT sqlc.arg('page_size');

-- An approval_decided event for each of these requests, on the run that
-- asked, for requests the brain answered itself: ended with their run, or
-- given up by a person who wrote again. Numbered after the run's last event,
-- in the order the requests were made. The caller holds the runs' session.
-- name: AppendDecidedEvents :exec
INSERT INTO run_events (run_id, seq, kind, payload)
SELECT a.run_id,
       coalesce((SELECT max(e.seq) FROM run_events e WHERE e.run_id = a.run_id), 0)
         + row_number() OVER (PARTITION BY a.run_id ORDER BY a.created_at, a.id),
       'approval_decided',
       jsonb_build_object('approval_id', a.id, 'decision', a.status,
                          'by', a.decided_by, 'via', a.decided_via)
         || CASE WHEN a.decision_scope IS NULL THEN '{}'::jsonb
                 ELSE jsonb_build_object('scope', a.decision_scope) END
         || CASE WHEN a.status = 'cancelled' AND a.decided_by IS NOT NULL
                 THEN '{"superseded": true}'::jsonb ELSE '{}'::jsonb END
FROM approvals a
WHERE a.id = ANY(sqlc.arg('ids')::uuid[]);

-- Whether a run called a tool not named: a run resumed from a wait has made
-- calls before, and a confirm may come only before any read.
-- name: RunCalledOtherThan :one
SELECT EXISTS (
    SELECT 1 FROM run_events
    WHERE run_id = sqlc.arg('run_id') AND kind = 'tool_call'
      AND NOT (payload->>'name' = ANY(sqlc.arg('names')::text[]))
)::bool AS called;
