-- name: ListUserTeams :many
SELECT t.id, t.name, tm.role
FROM team_members tm
JOIN teams t ON t.id = tm.team_id
WHERE tm.user_id = $1
AND t.deleted_at IS NULL
ORDER BY t.name;

-- name: AddTeamMember :one
INSERT INTO team_members (team_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: RemoveTeamMember :exec
DELETE FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: ListTeamMembers :many
SELECT u.id, u.subject, u.email, tm.role, u.name, (u.issuer = 'openarity:channel')::bool AS channel_user
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = sqlc.arg('team_id')
  AND (NOT sqlc.arg('use_cursor')::bool
       OR (u.subject, u.id) > (sqlc.arg('after_subject')::text, sqlc.arg('after_id')::uuid))
ORDER BY u.subject, u.id
LIMIT sqlc.arg('page_size');

-- FindTeamMember answers a question about somebody other than the caller,
-- whose own memberships are already on the request. Approving a channel sender
-- needs it: the person being named has to be in the channel's team, or the
-- approval grants them a voice in a team they do not belong to.
-- name: FindTeamMember :one
SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2;

-- LockTeamMembership serialises changes to one team's roles: demoting the
-- last admin is refused by counting the admins, and two demotions counted at
-- once would each see the other still there.
-- name: LockTeamMembership :exec
SELECT 1 FROM teams WHERE id = $1 FOR UPDATE;

-- name: FindTeamMemberUser :one
SELECT tm.role, (u.issuer = 'openarity:channel')::bool AS channel_user
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = $1 AND tm.user_id = $2;

-- name: CountTeamAdmins :one
SELECT count(*) FROM team_members WHERE team_id = $1 AND role = 'admin';

-- name: SetTeamMemberRole :exec
UPDATE team_members SET role = $3, updated_at = now() WHERE team_id = $1 AND user_id = $2;

-- SetChannelUserName renames a channel user; a user who logs in keeps the
-- name their identity provider gives.
-- name: SetChannelUserName :execrows
UPDATE users SET name = $2, updated_at = now() WHERE id = $1 AND issuer = 'openarity:channel';

-- DeleteChannelUser deletes a channel user removed from its team: it exists
-- for that team alone.
-- name: DeleteChannelUser :exec
DELETE FROM users WHERE id = $1 AND issuer = 'openarity:channel';
