-- name: CreateImprovementProposal :one
INSERT INTO improvement_proposals (
    team_id, agent_id, skill_id, kind, why, hypothesis, scope, signature,
    run_ids, outsider, marks, before, after, pass_id, goal, saves
) VALUES (
    sqlc.arg(team_id), sqlc.arg(agent_id), sqlc.narg(skill_id), sqlc.arg(kind),
    sqlc.arg(why), sqlc.arg(hypothesis), sqlc.arg(scope), sqlc.narg(signature),
    sqlc.arg(run_ids), sqlc.arg(outsider), sqlc.arg(marks), sqlc.arg(before), sqlc.arg(after),
    sqlc.narg(pass_id), sqlc.arg(goal), sqlc.arg(saves)::text[]
) RETURNING *;

-- name: GetImprovementProposal :one
SELECT * FROM improvement_proposals WHERE id = $1 AND team_id = $2;

-- Held for the rest of the transaction, so two decisions on one proposal
-- are made one after the other.
-- name: LockImprovementProposal :one
SELECT * FROM improvement_proposals WHERE id = $1 AND team_id = $2 FOR UPDATE;

-- One page, newest first, in the statuses asked for.
-- name: ListImprovementProposals :many
SELECT p.* FROM improvement_proposals p
WHERE p.team_id = sqlc.arg('team_id')
  AND p.status = ANY (sqlc.arg('statuses')::text[])
  AND (sqlc.narg('agent_id')::uuid IS NULL OR p.agent_id = sqlc.narg('agent_id')::uuid)
  AND strpos(lower(p.why || ' ' || p.hypothesis), lower(sqlc.arg('q')::text)) > 0
  -- Flagged: applied changes whose watch found them worse or costlier.
  AND (NOT sqlc.arg('flagged')::bool OR EXISTS (
       SELECT 1 FROM improvement_watches w WHERE w.proposal_id = p.id AND w.status = 'flagged'))
  AND (NOT sqlc.arg('use_cursor')::bool
   OR (p.created_at, p.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT sqlc.arg('page_size');

-- name: DecideImprovementProposal :one
UPDATE improvement_proposals
SET status = sqlc.arg('status'), decided_by = sqlc.arg('by'), decided_at = now()
WHERE id = sqlc.arg('id') AND status = 'waiting'
RETURNING *;

-- name: RevertImprovementProposal :one
UPDATE improvement_proposals
SET status = 'reverted', reverted_by = sqlc.arg('by'), reverted_at = now()
WHERE id = sqlc.arg('id') AND status = 'applied'
RETURNING *;

-- The other proposals waiting on the same target, which the one just applied
-- has made stale.
-- name: WithdrawImprovementProposals :exec
UPDATE improvement_proposals
SET status = 'withdrawn', withdrawn_by = sqlc.arg('applied_id')
WHERE team_id = sqlc.arg('team_id') AND status = 'waiting' AND id <> sqlc.arg('applied_id')
  AND CASE WHEN sqlc.narg('skill_id')::uuid IS NULL
           THEN kind <> 'skill_edit' AND agent_id = sqlc.arg('agent_id')
           ELSE skill_id = sqlc.narg('skill_id')::uuid END;

-- A target held for the rest of the transaction, so a person's edit and an
-- approval cannot interleave between the comparison and the write.
-- name: LockAgent :one
SELECT * FROM agents WHERE id = $1 AND team_id = $2 FOR UPDATE;

-- name: LockSkill :one
SELECT * FROM skills WHERE id = $1 AND team_id = $2 FOR UPDATE;

-- name: ListAgentToolNotes :many
SELECT * FROM agent_tool_notes WHERE agent_id = $1 ORDER BY mcp_server_id, tool;

-- name: ClearAgentToolNotes :exec
DELETE FROM agent_tool_notes WHERE agent_id = $1;

-- name: AddAgentToolNote :exec
INSERT INTO agent_tool_notes (agent_id, team_id, mcp_server_id, tool, note)
VALUES ($1, $2, $3, $4, $5);

-- Every agent a skill is granted to, by name: who a change to it reaches.
-- name: ListAgentsHoldingSkill :many
SELECT a.id, a.name FROM agents a
JOIN agent_skills g ON g.agent_id = a.id
WHERE g.skill_id = $1
ORDER BY a.name;

-- An applied skill_new records the agent as it was left and the skill it
-- made, which is what its revert compares and deletes.
-- name: SetImprovementProposalAfter :exec
UPDATE improvement_proposals SET after = sqlc.arg('after') WHERE id = sqlc.arg('id');

-- Names for the ids a proposal's changes carry, within the team. A deleted
-- one has no row, and is shown by its id.
-- name: ListTeamAgentNames :many
SELECT id, name FROM agents WHERE team_id = $1 AND id = ANY (sqlc.arg('ids')::uuid[]);

-- name: ListTeamSkillNames :many
SELECT id, name FROM skills WHERE team_id = $1 AND id = ANY (sqlc.arg('ids')::uuid[]);

-- name: ListTeamServerNames :many
SELECT id, name FROM mcp_servers WHERE team_id = $1 AND id = ANY (sqlc.arg('ids')::uuid[]);

-- Which of these runs the viewer may open: the rule GetRunVisible keeps.
-- name: ListVisibleRuns :many
SELECT r.id, r.session_id FROM runs r
WHERE r.team_id = sqlc.arg('team_id') AND r.id = ANY (sqlc.arg('ids')::uuid[])
  AND EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = r.session_id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid));
