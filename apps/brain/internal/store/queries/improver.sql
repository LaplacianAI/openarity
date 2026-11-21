-- Every pattern over the bar: an agent's digests that went wrong with one
-- key, counted by run, since the newest proposal on that key and since the
-- newest finished pass that was given it. Only custom agents in teams with
-- improvement on, and never what no setup change fixes.
-- name: ListPatterns :many
SELECT d.team_id, d.agent_id, d.part, d.target, d.cause, count(DISTINCT d.run_id)::int AS runs
FROM run_digests d
JOIN team_settings ts ON ts.team_id = d.team_id AND ts.improvement
JOIN agents a ON a.id = d.agent_id AND a.kind = 'custom'
WHERE d.went = 'wrong' AND d.part <> 'none' AND d.cause <> 'external'
  AND d.created_at > COALESCE((
        SELECT max(p.created_at) FROM improvement_proposals p
        WHERE p.agent_id = d.agent_id AND p.signature = d.part || ':' || d.target || ':' || d.cause),
      '-infinity'::timestamptz)
  AND d.created_at > COALESCE((
        SELECT max(x.started_at) FROM improver_passes x
        WHERE x.agent_id = d.agent_id AND x.status = 'done'
          AND (d.part || ':' || d.target || ':' || d.cause) = ANY (x.signatures)),
      '-infinity'::timestamptz)
GROUP BY d.team_id, d.agent_id, d.part, d.target, d.cause
HAVING count(DISTINCT d.run_id) >= sqlc.arg(min_runs)::int
ORDER BY d.agent_id, runs DESC, d.part, d.target, d.cause;

-- Every custom agent of a team with improvement on, with whether it has a
-- digest newer than its newest finished pass: what the daily sweep reads.
-- name: ListImprovingAgents :many
SELECT a.id, a.team_id,
       EXISTS (SELECT 1 FROM run_digests d
               WHERE d.agent_id = a.id
                 AND d.created_at > COALESCE((SELECT max(x.started_at) FROM improver_passes x
                                              WHERE x.agent_id = a.id AND x.status = 'done'),
                                             '-infinity'::timestamptz))::bool AS has_new
FROM agents a
JOIN team_settings ts ON ts.team_id = a.team_id AND ts.improvement
WHERE a.kind = 'custom'
ORDER BY a.team_id, a.id;

-- An agent's newest finished runs, top-level or handed work, for pruning.
-- name: ListAgentRecentRuns :many
SELECT id FROM runs
WHERE agent_id = sqlc.arg(agent_id) AND status IN ('succeeded', 'failed', 'cancelled')
ORDER BY finished_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- What those runs held and used: their started and tool_call events.
-- name: ListRunsUse :many
SELECT run_id, kind, payload FROM run_events
WHERE run_id = ANY (sqlc.arg(run_ids)::uuid[]) AND kind IN ('started', 'tool_call')
ORDER BY run_id, seq;

-- The prune candidates an agent was given, or proposed on, recently: not
-- offered again for a while.
-- name: ListRecentPruneLooks :many
SELECT unnest(x.pruned)::text AS candidate FROM improver_passes x
WHERE x.agent_id = sqlc.arg(agent_id)::uuid AND x.status = 'done' AND x.started_at > sqlc.arg(since)::timestamptz
UNION
SELECT substr(p.signature, 7)::text FROM improvement_proposals p
WHERE p.agent_id = sqlc.arg(agent_id)::uuid AND p.signature LIKE 'prune:%'
  AND (p.status IN ('rejected', 'reverted', 'waiting') OR p.created_at > sqlc.arg(since)::timestamptz);

-- Whether an agent has a pass under way, started recently enough to be
-- alive.
-- name: AgentPassRunning :one
SELECT EXISTS (SELECT 1 FROM improver_passes
               WHERE agent_id = sqlc.arg(agent_id) AND status = 'running'
                 AND started_at > sqlc.arg(since)::timestamptz)::bool;

-- name: CountTeamPassesSince :one
SELECT count(*)::int FROM improver_passes
WHERE team_id = sqlc.arg(team_id) AND started_at >= sqlc.arg(since)::timestamptz;

-- name: BeginImproverPass :one
INSERT INTO improver_passes (team_id, agent_id, trigger, signatures, pruned, run_ids, model)
VALUES (sqlc.arg(team_id), sqlc.arg(agent_id), sqlc.arg(trigger), sqlc.arg(signatures),
        sqlc.arg(pruned), sqlc.arg(run_ids), sqlc.arg(model))
RETURNING *;

-- name: FinishImproverPass :exec
UPDATE improver_passes
SET status = sqlc.arg(status), report = sqlc.arg(report), error = sqlc.narg(error),
    proposed = sqlc.arg(proposed), screened_out = sqlc.arg(screened_out),
    input_tokens = sqlc.arg(input_tokens), output_tokens = sqlc.arg(output_tokens),
    finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: GetImproverPass :one
SELECT * FROM improver_passes WHERE id = $1;

-- One page of a team's passes, newest first.
-- name: ListImproverPasses :many
SELECT * FROM improver_passes
WHERE team_id = sqlc.arg('team_id')
  AND (sqlc.narg('agent_id')::uuid IS NULL OR agent_id = sqlc.narg('agent_id')::uuid)
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (started_at, id) < (sqlc.arg('after_started_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY started_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- An agent's digests with one key that went wrong, newest first, with how
-- each run was judged and what members said.
-- name: ListPatternDigests :many
SELECT d.run_id, d.summary, d.behaviour, d.outsider, d.created_at,
       o.verdict, o.missed,
       (SELECT count(*) FILTER (WHERE f.value = 'down') FROM run_feedback f WHERE f.run_id = d.run_id)::int AS down,
       (SELECT count(*) FILTER (WHERE f.value = 'up') FROM run_feedback f WHERE f.run_id = d.run_id)::int AS up
FROM run_digests d JOIN run_outcomes o ON o.run_id = d.run_id
WHERE d.agent_id = sqlc.arg(agent_id) AND d.went = 'wrong'
  AND d.part = sqlc.arg(part) AND d.target = sqlc.arg(target) AND d.cause = sqlc.arg(cause)
ORDER BY d.created_at DESC, d.run_id
LIMIT sqlc.arg(page_size);

-- An agent's newest digests that went well, those on one part and target
-- first: what it does well, to contrast with.
-- name: ListWentWell :many
SELECT d.run_id, d.summary, d.behaviour, d.part, d.target, d.cause, d.outsider, d.created_at
FROM run_digests d
WHERE d.agent_id = sqlc.arg(agent_id) AND d.went = 'well'
ORDER BY (d.part = sqlc.arg(part) AND d.target = sqlc.arg(target)) DESC, d.created_at DESC, d.run_id
LIMIT sqlc.arg(page_size);

-- An agent's newest digests that went wrong, of any key: a daily pass's
-- failures when no pattern crossed the bar.
-- name: ListWentWrong :many
SELECT d.run_id, d.summary, d.behaviour, d.part, d.target, d.cause, d.outsider, d.created_at
FROM run_digests d
WHERE d.agent_id = sqlc.arg(agent_id) AND d.went = 'wrong'
ORDER BY d.created_at DESC, d.run_id
LIMIT sqlc.arg(page_size);

-- Whether runs' digests for an agent had an outsider: any of them.
-- name: AnyDigestOutsider :one
SELECT EXISTS (SELECT 1 FROM run_digests
               WHERE agent_id = sqlc.arg(agent_id) AND run_id = ANY (sqlc.arg(run_ids)::uuid[])
                 AND outsider)::bool;

-- An agent's own record: its newest proposals.
-- name: ListAgentProposalRecord :many
SELECT id, kind, signature, status, why, hypothesis, created_at, decided_at, reverted_at
FROM improvement_proposals
WHERE agent_id = sqlc.arg(agent_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- The team's newest applied changes on other agents.
-- name: ListTeamAppliedElsewhere :many
SELECT p.kind, p.why, a.name AS agent
FROM improvement_proposals p JOIN agents a ON a.id = p.agent_id
WHERE p.team_id = sqlc.arg(team_id) AND p.agent_id <> sqlc.arg(agent_id) AND p.status = 'applied'
ORDER BY p.decided_at DESC, p.id DESC
LIMIT sqlc.arg(page_size);

-- Proposals one pass stored on one target already.
-- name: CountPassProposalsOnTarget :one
SELECT count(*)::int FROM improvement_proposals
WHERE pass_id = sqlc.arg(pass_id)
  AND CASE WHEN sqlc.narg('skill_id')::uuid IS NULL
           THEN kind <> 'skill_edit' AND agent_id = sqlc.arg('agent_id')
           ELSE skill_id = sqlc.narg('skill_id')::uuid END;

-- The team's MCP servers with the names of their discovered tools.
-- name: ListTeamServersWithTools :many
SELECT s.id, s.name, s.bare,
       COALESCE(array_agg(t.name ORDER BY t.name) FILTER (WHERE t.name IS NOT NULL), '{}')::text[] AS tools
FROM mcp_servers s LEFT JOIN mcp_tools t ON t.mcp_server_id = s.id
WHERE s.team_id = $1
GROUP BY s.id, s.name, s.bare
ORDER BY s.name;
