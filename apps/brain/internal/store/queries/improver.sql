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
        WHERE x.agent_id = d.agent_id AND x.status = 'done' AND (NOT x.stopped OR x.proposed > 0)
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
                                              WHERE x.agent_id = a.id AND x.status = 'done' AND (NOT x.stopped OR x.proposed > 0)),
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

-- What those runs held and used: the skills and tools each started with,
-- and the name of each tool called, with the arguments only of the tools
-- that name another (a skill opened, a tool called through call_tool).
-- name: ListRunsUse :many
SELECT run_id, kind,
       ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(payload->'skills') = 'array'
                                                   THEN payload->'skills' ELSE '[]'::jsonb END))::text[] AS skills,
       ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(payload->'tools') = 'array'
                                                   THEN payload->'tools' ELSE '[]'::jsonb END))::text[] AS tools,
       COALESCE(payload->>'name', '')::text AS name,
       (CASE WHEN payload->>'name' = ANY (sqlc.arg(naming_tools)::text[])
             THEN COALESCE(payload->>'arguments', '') ELSE '' END)::text AS arguments
FROM run_events
WHERE run_id = ANY (sqlc.arg(run_ids)::uuid[]) AND kind IN ('started', 'tool_call')
ORDER BY run_id, seq;

-- The prune candidates an agent was given, or proposed on, recently: not
-- offered again for a while.
-- name: ListRecentPruneLooks :many
SELECT unnest(x.pruned)::text AS candidate FROM improver_passes x
WHERE x.agent_id = sqlc.arg(agent_id)::uuid AND x.status = 'done' AND (NOT x.stopped OR x.proposed > 0) AND x.started_at > sqlc.arg(since)::timestamptz
UNION
SELECT substr(p.signature, 7)::text FROM improvement_proposals p
WHERE p.agent_id = sqlc.arg(agent_id)::uuid AND p.signature LIKE 'prune:%'
  AND (p.status IN ('rejected', 'reverted', 'waiting') OR p.created_at > sqlc.arg(since)::timestamptz);

-- Whether an agent has a pass under way.
-- name: AgentPassRunning :one
SELECT EXISTS (SELECT 1 FROM improver_passes
               WHERE agent_id = sqlc.arg(agent_id) AND status = 'running')::bool;

-- name: CountTeamPassesSince :one
SELECT count(*)::int FROM improver_passes
WHERE team_id = sqlc.arg(team_id) AND started_at >= sqlc.arg(since)::timestamptz;

-- No row when the agent already has a pass running, or the workflow has
-- begun its pass: the caller reads which.
-- name: BeginImproverPass :one
INSERT INTO improver_passes (team_id, agent_id, trigger, signatures, pruned, run_ids, model, workflow_id)
VALUES (sqlc.arg(team_id), sqlc.arg(agent_id), sqlc.arg(trigger), sqlc.arg(signatures),
        sqlc.arg(pruned), sqlc.arg(run_ids), sqlc.arg(model), sqlc.arg(workflow_id))
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetImproverPassByWorkflow :one
SELECT * FROM improver_passes WHERE workflow_id = $1;

-- An attempt beginning: the pass is alive, and draws on these runs. No row
-- when it is no longer running: failed as lost meanwhile.
-- name: TouchImproverPass :execrows
UPDATE improver_passes SET alive_at = now(), drew_on = sqlc.arg(drew_on)
WHERE id = sqlc.arg(id) AND status = 'running';

-- Passes whose worker stopped answering, failed so the agent may have
-- another.
-- name: FailLostImproverPasses :execrows
UPDATE improver_passes
SET status = 'failed', error = sqlc.arg(error), finished_at = now()
WHERE status = 'running' AND alive_at < now() - make_interval(mins => sqlc.arg(silent_minutes)::int);

-- What an attempt spent, added whether or not it finished.
-- name: AddImproverPassUsage :exec
UPDATE improver_passes
SET input_tokens = input_tokens + sqlc.arg(input_tokens),
    output_tokens = output_tokens + sqlc.arg(output_tokens),
    screened_out = screened_out + sqlc.arg(screened_out)
WHERE id = sqlc.arg(id);

-- name: FinishImproverPass :exec
UPDATE improver_passes
SET status = sqlc.arg(status), report = sqlc.arg(report), error = sqlc.narg(error),
    stopped = sqlc.arg(stopped),
    proposed = (SELECT count(*) FROM improvement_proposals WHERE pass_id = sqlc.arg(id)::uuid),
    finished_at = now()
WHERE id = sqlc.arg(id)::uuid AND status = 'running';

-- name: CountPassProposals :one
SELECT count(*)::int FROM improvement_proposals WHERE pass_id = $1;

-- What an earlier pass left waiting on the same pattern, the same kind and
-- the same target, which a newer proposal replaces. A person's own, and a
-- pass's on another pattern, wait on.
-- name: WithdrawSupersededProposals :exec
UPDATE improvement_proposals
SET status = 'withdrawn', withdrawn_by = sqlc.arg(newer_id)
WHERE team_id = sqlc.arg(team_id) AND agent_id = sqlc.arg(agent_id) AND status = 'waiting'
  AND id <> sqlc.arg(newer_id) AND pass_id IS NOT NULL
  AND kind = sqlc.arg(kind) AND signature = sqlc.arg(signature)
  AND skill_id IS NOT DISTINCT FROM sqlc.narg(skill_id)::uuid;

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
