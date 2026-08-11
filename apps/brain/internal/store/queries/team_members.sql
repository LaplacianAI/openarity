-- name: ListUserTeams :many
SELECT t.id, t.name, tm.role
FROM team_members tm
JOIN teams t ON t.id = tm.team_id
WHERE tm.user_id = $1
ORDER BY t.name;

-- name: AddTeamMember :one
INSERT INTO team_members (team_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: RemoveTeamMember :exec
DELETE FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: ListTeamMembers :many
SELECT u.id, u.subject, u.email, tm.role
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
       OR (u.subject, u.id) > (sqlc.arg('after_subject')::text, sqlc.arg('after_id')::uuid))
ORDER BY u.subject, u.id
LIMIT sqlc.arg('page_size');
