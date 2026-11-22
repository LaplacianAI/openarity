-- A change just applied is watched from now, on the agents it reaches.
-- name: StartImprovementWatch :exec
INSERT INTO improvement_watches (proposal_id, team_id, agent_ids, started_at)
VALUES (sqlc.arg(proposal_id), sqlc.arg(team_id), sqlc.arg(agent_ids)::uuid[], now());

-- Every agent a skill is granted to: who a skill edit reaches.
-- name: ListSkillHolders :many
SELECT agent_id FROM agent_skills WHERE skill_id = $1 ORDER BY agent_id;

-- A reverted change's watch ends, whatever it had come to, keeping what it
-- counted and any flag, so the record says it was flagged and then reverted.
-- name: EndRevertedWatch :exec
UPDATE improvement_watches SET status = 'ended', ended_why = 'reverted', finished_at = coalesce(finished_at, now())
WHERE proposal_id = $1 AND status <> 'ended';

-- A person looked at a flagged change and kept it. No row unless flagged.
-- name: ClearImprovementWatch :one
UPDATE improvement_watches SET status = 'cleared', cleared_by = sqlc.arg(by), cleared_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND team_id = sqlc.arg(team_id) AND status = 'flagged'
RETURNING proposal_id;

-- name: GetImprovementWatch :one
SELECT * FROM improvement_watches WHERE proposal_id = $1;

-- The watches of a page of proposals.
-- name: ListImprovementWatches :many
SELECT * FROM improvement_watches WHERE proposal_id = ANY (sqlc.arg(ids)::uuid[]);

-- One page of open watches, the least recently swept first, so every watch
-- is reached in turn. Each counts the time since its last sweep toward its
-- 30 days, but only while its team has improvement on; a watch in a team
-- with it off is marked swept and not counted, and comes back with on false.
-- name: TickOpenWatches :many
WITH page AS MATERIALIZED (
    SELECT x.proposal_id FROM improvement_watches x
    WHERE x.status = 'watching'
    ORDER BY x.last_swept_at NULLS FIRST, x.started_at
    LIMIT sqlc.arg(page_size)
    FOR UPDATE SKIP LOCKED
), ticked AS (
    UPDATE improvement_watches w SET
        on_seconds = w.on_seconds + CASE
            WHEN EXISTS (SELECT 1 FROM team_settings ts WHERE ts.team_id = w.team_id AND ts.improvement)
            THEN least(extract(epoch FROM now() - coalesce(w.last_swept_at, now())), 2147483647 - w.on_seconds)::int
            ELSE 0 END,
        last_swept_at = now()
    FROM page
    WHERE w.proposal_id = page.proposal_id
    RETURNING w.*
)
SELECT t.*, EXISTS (SELECT 1 FROM team_settings ts WHERE ts.team_id = t.team_id AND ts.improvement)::bool AS improving
FROM ticked t;

-- Judged top-level runs whose trees reach the watched agents, either side of
-- when the change was applied: before newest first, after oldest first. Each
-- with its verdict and votes, what its tree spent (a cached token as a
-- tenth, as the budget counts) and its steps, and whether a digest of it
-- carries the targeted signature.
-- name: ListWatchRuns :many
SELECT r.id, r.created_at, o.verdict,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'up')::int AS up,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'down')::int AS down,
       (SELECT coalesce(sum(run_spent(t.input_tokens, t.cached_input_tokens, t.output_tokens)), 0)
          FROM runs t WHERE t.root_run_id = r.id)::bigint AS spent,
       (SELECT count(*) FROM run_events e JOIN runs t ON t.id = e.run_id
          WHERE t.root_run_id = r.id AND e.kind = 'step')::int AS steps,
       EXISTS (SELECT 1 FROM run_digests d
               WHERE d.run_id = r.id AND d.agent_id = ANY (sqlc.arg(agent_ids)::uuid[])
                 AND d.part = sqlc.arg(part)::text AND d.target = sqlc.arg(target)::text
                 AND d.cause = sqlc.arg(cause)::text) AS has_signature
FROM runs r
JOIN run_outcomes o ON o.run_id = r.id
WHERE r.team_id = sqlc.arg(team_id) AND r.parent_run_id IS NULL
  AND EXISTS (SELECT 1 FROM runs t WHERE t.root_run_id = r.id AND t.agent_id = ANY (sqlc.arg(agent_ids)::uuid[]))
  AND CASE WHEN sqlc.arg(after)::bool THEN r.created_at > sqlc.arg(at)::timestamptz
           ELSE r.created_at <= sqlc.arg(at)::timestamptz END
ORDER BY CASE WHEN sqlc.arg(after)::bool THEN r.created_at END ASC,
         CASE WHEN NOT sqlc.arg(after)::bool THEN r.created_at END DESC
LIMIT sqlc.arg(page_size);

-- Other changes applied to any of the agents between from and until.
-- name: ListOverlappingWatches :many
SELECT proposal_id FROM improvement_watches
WHERE team_id = sqlc.arg(team_id) AND agent_ids && sqlc.arg(agent_ids)::uuid[]
  AND proposal_id <> sqlc.arg(proposal_id)
  AND started_at BETWEEN sqlc.arg(since)::timestamptz AND sqlc.arg(until)::timestamptz
ORDER BY started_at DESC;

-- What the watch has counted so far, while it watches.
-- name: SetWatchProgress :exec
UPDATE improvement_watches SET
    baseline_runs = sqlc.arg(baseline_runs), baseline_bad = sqlc.arg(baseline_bad), bar = sqlc.narg(bar),
    before_runs = sqlc.arg(before_runs), before_bad = sqlc.arg(before_bad),
    before_up = sqlc.arg(before_up), before_down = sqlc.arg(before_down),
    after_runs = sqlc.arg(after_runs), after_bad = sqlc.arg(after_bad),
    after_up = sqlc.arg(after_up), after_down = sqlc.arg(after_down),
    before_tokens = sqlc.narg(before_tokens), after_tokens = sqlc.narg(after_tokens),
    baseline_p90 = sqlc.narg(baseline_p90),
    before_steps = sqlc.narg(before_steps), after_steps = sqlc.narg(after_steps),
    sig_before = sqlc.narg(sig_before), sig_after = sqlc.narg(sig_after),
    overlapping = sqlc.arg(overlapping)::uuid[]
WHERE proposal_id = sqlc.arg(proposal_id) AND status = 'watching';

-- The verdict, written once: a second sweep over the same window finds the
-- watch no longer watching and writes nothing.
-- name: FinishImprovementWatch :execrows
UPDATE improvement_watches SET status = sqlc.arg(status), flag = sqlc.narg(flag),
    ended_why = sqlc.narg(ended_why), finished_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND status = 'watching';
