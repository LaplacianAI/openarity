-- name: ListRolePermissions :many
SELECT action FROM role_permissions WHERE role = $1;

-- name: ListRoles :many
SELECT * FROM roles ORDER BY name;
