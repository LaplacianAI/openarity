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

-- name: ListRunEvents :many
SELECT * FROM run_events
WHERE run_id = sqlc.arg('run_id') AND seq > sqlc.arg('after')::int
ORDER BY seq
LIMIT sqlc.arg('page_size');
