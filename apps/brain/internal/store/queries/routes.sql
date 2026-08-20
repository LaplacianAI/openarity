-- name: ListRoutePermissions :many
SELECT method, path, permission, scope
FROM route_permissions
ORDER BY path, method;