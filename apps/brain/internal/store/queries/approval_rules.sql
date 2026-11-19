-- A second rule for the same target, in any case, is a 23505 on
-- approval_rules_target_key: a target has one standing answer.
-- name: CreateApprovalRule :one
INSERT INTO approval_rules (team_id, target_kind, target, mode, require_other)
VALUES (sqlc.arg(team_id), sqlc.arg(target_kind), sqlc.arg(target), sqlc.arg(mode), sqlc.arg(require_other))
RETURNING *;

-- name: ListApprovalRules :many
SELECT * FROM approval_rules WHERE team_id = $1 ORDER BY target_kind, lower(target);

-- name: DeleteApprovalRule :execrows
DELETE FROM approval_rules WHERE id = $1 AND team_id = $2;

-- A rule a blueprint made stricter, in place.
-- name: UpdateApprovalRule :one
UPDATE approval_rules SET mode = sqlc.arg('mode'), require_other = sqlc.arg('require_other')
WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id')
RETURNING *;
