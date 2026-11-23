-- The custom agents an efficiency sweep may look at: in teams with both
-- improvement and its efficiency pass on, with no change of theirs waiting
-- for a person and none still watched or flagged. Each with the team's
-- numbers. A failure pattern over the bar is checked by the caller.
-- name: ListEfficiencyAgents :many
SELECT a.id, a.team_id, a.max_steps,
       ts.improvement_efficiency_runs AS min_runs, ts.improvement_efficiency_days AS days,
       ts.improvement_efficiency_spread AS spread, ts.improvement_efficiency_mark_runs AS mark_runs
FROM agents a
JOIN team_settings ts ON ts.team_id = a.team_id AND ts.improvement AND ts.improvement_efficiency
WHERE a.kind = 'custom'
  AND NOT EXISTS (SELECT 1 FROM improvement_proposals p WHERE p.agent_id = a.id AND p.status = 'waiting')
  AND NOT EXISTS (SELECT 1 FROM improvement_watches w
                  WHERE w.status IN ('watching', 'flagged') AND a.id = ANY (w.agent_ids))
ORDER BY a.team_id, a.id;

-- Judged top-level runs whose trees reach the agent, newest first, from the
-- last days: each with its verdict and votes, what its tree spent (a cached
-- token as a tenth) and how long it worked.
-- name: ListEfficiencyRuns :many
SELECT r.id, r.created_at, o.verdict,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'up')::int AS up,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'down')::int AS down,
       (SELECT coalesce(sum(run_spent(t.input_tokens, t.cached_input_tokens, t.output_tokens)), 0)
          FROM runs t WHERE t.root_run_id = r.id)::bigint AS spent,
       run_work_ms(r.id)::bigint AS work_ms
FROM runs r
JOIN run_outcomes o ON o.run_id = r.id
WHERE r.team_id = sqlc.arg(team_id) AND r.parent_run_id IS NULL
  AND r.created_at > now() - make_interval(days => sqlc.arg(days)::int)
  AND EXISTS (SELECT 1 FROM runs t WHERE t.root_run_id = r.id AND t.agent_id = sqlc.arg(agent_id))
ORDER BY r.created_at DESC
LIMIT sqlc.arg(page_size);

-- What those runs' trees did, in the order it was written: each step, each
-- tool offered at the start, each call (its arguments as a hash) and each
-- result, with whether it failed and how long it took.
-- name: ListEfficiencyEvents :many
SELECT t.root_run_id AS root_id, e.run_id, e.kind,
       COALESCE(e.payload->>'id', '')::text AS call_id,
       COALESCE(e.payload->>'name', '')::text AS name,
       md5(COALESCE(e.payload->>'arguments', ''))::text AS args_hash,
       (COALESCE(e.payload->>'error', '') <> '')::bool AS failed,
       (CASE WHEN jsonb_typeof(e.payload->'duration_ms') = 'number'
             THEN (e.payload->>'duration_ms')::numeric ELSE 0 END)::bigint AS duration_ms,
       (CASE WHEN jsonb_typeof(e.payload->'tools') = 'array'
             THEN jsonb_array_length(e.payload->'tools') ELSE 0 END)::int AS tools
FROM run_events e
JOIN runs t ON t.id = e.run_id
WHERE t.root_run_id = ANY (sqlc.arg(root_ids)::uuid[])
  AND e.kind IN ('started', 'step', 'tool_call', 'tool_result')
ORDER BY t.root_run_id, e.created_at, e.run_id, e.seq;

-- The same, for the runs an efficiency pass was given, whenever it runs.
-- name: ListRunsCost :many
SELECT r.id, r.created_at, o.verdict,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'up')::int AS up,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'down')::int AS down,
       (SELECT coalesce(sum(run_spent(t.input_tokens, t.cached_input_tokens, t.output_tokens)), 0)
          FROM runs t WHERE t.root_run_id = r.id)::bigint AS spent,
       run_work_ms(r.id)::bigint AS work_ms
FROM runs r
JOIN run_outcomes o ON o.run_id = r.id
WHERE r.id = ANY (sqlc.arg(ids)::uuid[])
ORDER BY r.created_at DESC;
