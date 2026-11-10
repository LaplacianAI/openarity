-- A blueprint is planned against everything its team holds, read table by
-- table rather than agent by agent.

-- name: ListTeamAgents :many
SELECT * FROM agents WHERE team_id = $1 ORDER BY name;

-- name: ListTeamAgentServers :many
SELECT * FROM agent_mcp_servers WHERE team_id = $1;

-- name: ListTeamAgentSkills :many
SELECT * FROM agent_skills WHERE team_id = $1;

-- name: ListTeamAgentDelegates :many
SELECT * FROM agent_delegates WHERE team_id = $1;

-- name: ListTeamMCPServers :many
SELECT * FROM mcp_servers WHERE team_id = $1 ORDER BY name;

-- name: ListTeamSkills :many
SELECT * FROM skills WHERE team_id = $1 ORDER BY name;

-- name: ListTeamSkillFiles :many
SELECT skill_id, path, sha256 FROM skill_files WHERE team_id = $1;
