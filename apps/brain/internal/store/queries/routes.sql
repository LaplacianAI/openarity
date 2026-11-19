-- name: ListRoutePermissions :many
-- Byte order, as Go compares strings: a locale's collation skips punctuation,
-- so "approval-settings" and "approvals/" would come back in an order that
-- changes with the database's locale.
SELECT method, path, permission, scope
FROM route_permissions
ORDER BY path COLLATE "C", method COLLATE "C";
