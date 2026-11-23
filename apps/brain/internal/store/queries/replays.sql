-- What a replay may spend, and whether it runs at all.
-- name: GetReplaySettings :one
SELECT improvement, improvement_replay, improvement_replay_runs, improvement_replay_tokens,
       improvement_replay_minutes, improvement_replay_daily
FROM team_settings WHERE team_id = $1;

-- Past runs a replay may be tried on, with their outcome and whether they
-- can be rebuilt as they were: a clipped call cannot be served back whole,
-- and a compaction in the conversation from the run's start on rewrote the
-- transcript it began from.
-- name: ListReplayRuns :many
SELECT sqlc.embed(r), o.verdict,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'up')::int AS up,
       (SELECT count(*) FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'down')::int AS down,
       EXISTS (SELECT 1 FROM run_events e WHERE e.run_id = r.id AND e.kind IN ('tool_call', 'tool_result')
                 AND coalesce((e.payload ->> 'truncated')::bool, false)) AS clipped,
       EXISTS (SELECT 1 FROM run_events e JOIN runs t ON t.id = e.run_id
               WHERE t.transcript = r.transcript AND t.created_at >= r.created_at
                 AND e.kind = 'compacted') AS compacted
FROM runs r
JOIN run_outcomes o ON o.run_id = r.id
WHERE r.team_id = sqlc.arg(team_id) AND r.id = ANY (sqlc.arg(ids)::uuid[]);

-- The agent's recent good runs a proposal does not cite, newest first, one
-- a conversation: good as I2's Effective has it, so a 👎 makes a run bad and
-- a 👍 makes it good whatever the verdict.
-- name: ListReplayGoodRunIDs :many
SELECT id FROM (
    SELECT DISTINCT ON (r.session_id) r.id, r.created_at
    FROM runs r
    JOIN run_outcomes o ON o.run_id = r.id
    WHERE r.team_id = sqlc.arg(team_id) AND r.agent_id = sqlc.arg(agent_id)
      AND r.parent_run_id IS NULL AND NOT r.builder AND NOT r.hub
      AND r.status IN ('succeeded', 'failed', 'cancelled')
      AND r.created_at > sqlc.arg(since)::timestamptz
      AND NOT (r.id = ANY (sqlc.arg(exclude)::uuid[]))
      AND NOT EXISTS (SELECT 1 FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'down')
      AND (o.verdict = 'done' OR EXISTS (SELECT 1 FROM run_feedback f WHERE f.run_id = r.id AND f.value = 'up'))
    ORDER BY r.session_id, r.created_at DESC
) newest
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size);

-- name: InsertReplay :exec
INSERT INTO improvement_replays (proposal_id, team_id, source_run_id, position, chosen, original,
                                 status, skip_why, finished_at)
VALUES (sqlc.arg(proposal_id), sqlc.arg(team_id), sqlc.arg(source_run_id), sqlc.arg(position),
        sqlc.arg(chosen), sqlc.arg(original), sqlc.arg(status), sqlc.narg(skip_why),
        CASE WHEN sqlc.arg(status)::text = 'skipped' THEN now() END)
ON CONFLICT (proposal_id, source_run_id) DO NOTHING;

-- name: ListQueuedReplays :many
SELECT source_run_id FROM improvement_replays
WHERE proposal_id = $1 AND status IN ('queued', 'trying')
ORDER BY position;

-- A try begins, unless the team has used its tries for the day: the count
-- and the claim are one statement, so two workers cannot both take the last.
-- name: StartReplay :execrows
UPDATE improvement_replays SET status = 'trying', started_at = now()
WHERE proposal_id = sqlc.arg(proposal_id)::uuid AND source_run_id = sqlc.arg(source_run_id)::uuid
  AND status IN ('queued', 'trying')
  AND (SELECT count(*) FROM improvement_replays d
       WHERE d.team_id = improvement_replays.team_id AND d.started_at >= sqlc.arg(since)::timestamptz
         AND NOT (d.proposal_id = sqlc.arg(proposal_id)::uuid AND d.source_run_id = sqlc.arg(source_run_id)::uuid))
      < sqlc.arg(daily)::int;

-- name: FinishReplay :exec
UPDATE improvement_replays
SET status = 'done', replayed = sqlc.arg(replayed), verdict = sqlc.narg(verdict), why = sqlc.narg(why),
    diverged = sqlc.arg(diverged), recorded = sqlc.arg(recorded), live = sqlc.arg(live),
    not_made = sqlc.arg(not_made), input_tokens = sqlc.arg(input_tokens),
    cached_input_tokens = sqlc.arg(cached_input_tokens), output_tokens = sqlc.arg(output_tokens), judge_tokens = sqlc.arg(judge_tokens),
    steps = sqlc.arg(steps), work_ms = sqlc.arg(work_ms), answer = sqlc.narg(answer),
    trace = sqlc.arg(trace), finished_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND source_run_id = sqlc.arg(source_run_id) AND status = 'trying';

-- name: FailReplay :exec
UPDATE improvement_replays
SET status = 'failed', error = sqlc.arg(error), input_tokens = sqlc.arg(input_tokens),
    output_tokens = sqlc.arg(output_tokens), finished_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND source_run_id = sqlc.arg(source_run_id)
  AND status IN ('queued', 'trying');

-- name: SkipReplay :exec
UPDATE improvement_replays SET status = 'skipped', skip_why = sqlc.arg(skip_why), finished_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND source_run_id = sqlc.arg(source_run_id)
  AND status IN ('queued', 'trying');

-- Every try of a proposal still to run is skipped, saying why.
-- name: SkipQueuedReplays :exec
UPDATE improvement_replays SET status = 'skipped', skip_why = sqlc.arg(skip_why), finished_at = now()
WHERE proposal_id = sqlc.arg(proposal_id) AND status IN ('queued', 'trying');

-- A proposal's tries, without their traces, in their order.
-- name: ListProposalReplays :many
SELECT p.proposal_id, p.source_run_id, p.position, p.chosen, p.status, p.skip_why, p.error,
       p.original, p.replayed, p.verdict, p.why, p.diverged, p.recorded, p.live, p.not_made,
       run_spent(p.input_tokens, p.cached_input_tokens, p.output_tokens)::bigint AS spent,
       p.judge_tokens, p.steps, p.work_ms,
       p.started_at, p.finished_at,
       r.input AS request, r.session_id,
       (SELECT coalesce(sum(run_spent(t.input_tokens, t.cached_input_tokens, t.output_tokens)), 0)
          FROM runs t WHERE t.root_run_id = r.id)::bigint AS original_spent,
       (SELECT count(*) FROM run_events e JOIN runs t ON t.id = e.run_id
          WHERE t.root_run_id = r.id AND e.kind = 'step')::int AS original_steps,
       run_work_ms(r.id)::bigint AS original_work_ms
FROM improvement_replays p
JOIN runs r ON r.id = p.source_run_id
WHERE p.proposal_id = ANY (sqlc.arg(proposal_ids)::uuid[])
ORDER BY p.proposal_id, p.position;

-- name: GetProposalReplay :one
SELECT p.*, r.session_id, r.input AS request FROM improvement_replays p
JOIN runs r ON r.id = p.source_run_id
WHERE p.team_id = sqlc.arg(team_id) AND p.proposal_id = sqlc.arg(proposal_id)
  AND p.source_run_id = sqlc.arg(source_run_id);

-- The servers a candidate harness grants, as ListGrantedMCPServers reads
-- the agent's own: a replay tries grants not yet made.
-- name: ListCandidateMCPServers :many
SELECT sqlc.embed(s), c.status AS oauth_status
FROM mcp_servers s
LEFT JOIN mcp_oauth_connections c ON c.server_id = s.id
WHERE s.team_id = sqlc.arg(team_id) AND s.id = ANY (sqlc.arg(ids)::uuid[])
ORDER BY s.name;

-- name: ListCandidateMCPTools :many
SELECT t.id, t.mcp_server_id, t.name, t.description, t.input_schema, t.annotations
FROM mcp_tools t
WHERE t.mcp_server_id = ANY (sqlc.arg(ids)::uuid[])
ORDER BY t.mcp_server_id, t.name;

-- Where a run began in its conversation: what a replay of it may read of
-- the conversation is what was said before.
-- name: GetRunStartedSeq :one
SELECT coalesce(min(session_seq), 0)::bigint FROM run_events WHERE run_id = $1 AND session_seq IS NOT NULL;
