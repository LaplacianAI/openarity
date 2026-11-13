-- A session's open proposal gives way to the next. Run in the transaction
-- that creates it, so a session never holds two open proposals: one statement
-- cannot do both, since its insert is checked against the row before update.
-- name: SupersedeProposals :exec
UPDATE proposals SET state = 'superseded' WHERE session_id = $1 AND state = 'open';

-- name: CreateProposal :one
INSERT INTO proposals (team_id, session_id, run_id, summary, blueprint, plan)
VALUES (sqlc.arg('team_id')::uuid, sqlc.arg('session_id')::uuid, sqlc.arg('run_id')::uuid,
        sqlc.arg('summary')::text, sqlc.arg('blueprint')::jsonb, sqlc.arg('plan')::jsonb)
RETURNING *;

-- name: CountRunProposals :one
SELECT count(*) FROM proposals WHERE run_id = $1;

-- A proposal is seen by whoever may see its session, as its run is: a direct
-- session is its person's, and a moderator's.
-- name: GetProposalVisible :one
SELECT p.* FROM proposals p
WHERE p.id = sqlc.arg('id') AND p.team_id = sqlc.arg('team_id')
  AND EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = p.session_id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid));

-- Newest first, a page at a time; one session's when it is named.
-- name: ListProposalsVisible :many
SELECT p.* FROM proposals p
WHERE p.team_id = sqlc.arg('team_id')
  AND (sqlc.narg('session_id')::uuid IS NULL OR p.session_id = sqlc.narg('session_id'))
  AND (NOT sqlc.arg('use_cursor')::bool
       OR (p.created_at, p.id) < (sqlc.arg('after_created_at')::timestamptz, sqlc.arg('after_id')::uuid))
  AND EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = p.session_id
      AND (s.kind <> 'direct'
       OR sqlc.arg('moderator')::bool
       OR s.user_id = sqlc.arg('viewer')::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT sqlc.arg('page_size');

-- Only an open proposal is applied, once: no row means it was applied or
-- superseded meanwhile, and the apply it belongs to is rolled back.
-- name: MarkProposalApplied :one
UPDATE proposals SET state = 'applied', applied_by = sqlc.narg('applied_by'), applied_at = now()
WHERE id = sqlc.arg('id') AND state = 'open'
RETURNING *;

-- name: CreateBuilderRun :one
INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, user_id, input, builder, transcript)
SELECT sqlc.arg('id')::uuid, t.id, sqlc.arg('session_id')::uuid, sqlc.arg('id')::uuid,
       'builder', sqlc.narg('user_id')::uuid, sqlc.arg('input')::text, true,
       'session:' || sqlc.arg('session_id')::uuid::text
FROM teams t
WHERE t.id = sqlc.arg('team_id')::uuid
RETURNING *;

-- The tools each of a team's MCP servers offered when last discovered,
-- by name, for the builder to choose an allow list from.
-- name: ListTeamToolNames :many
SELECT s.name AS server, t.name AS tool FROM mcp_tools t
JOIN mcp_servers s ON s.id = t.mcp_server_id
WHERE s.team_id = $1
ORDER BY s.name, t.name;
