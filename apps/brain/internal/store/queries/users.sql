-- name: UpsertUser :one
INSERT INTO users (issuer, subject, email)
VALUES ($1, $2, $3)
ON CONFLICT (issuer, subject) DO UPDATE
SET email      = EXCLUDED.email,
    updated_at = now()
RETURNING *;

-- name: LockFirstUserBootstrap :exec
-- Serialises the check-then-promote below. Without it two logins arriving
-- together both read "no super admin" and both promote, because they update
-- different rows and so never contend for a row lock. Transaction-scoped, so
-- it is released by the commit that writes the grant.
SELECT pg_advisory_xact_lock(sqlc.arg('key')::bigint);

-- name: AnySuperAdmin :one
-- Whether anybody holds the grant. Asked before promoting, so it must be the
-- whole table rather than one row: the guard is "this install has no admin",
-- not "this user is not one".
SELECT EXISTS (SELECT 1 FROM users WHERE is_super_admin) AS present;

-- name: PromoteToSuperAdmin :one
UPDATE users SET is_super_admin = true, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT id, issuer, subject, email
FROM users
WHERE (NOT sqlc.arg('use_subject')::bool OR subject = sqlc.arg('subject')::text)
  AND (NOT sqlc.arg('use_cursor')::bool
       OR (subject, id) > (sqlc.arg('after_subject')::text, sqlc.arg('after_id')::uuid))
ORDER BY subject, id
LIMIT sqlc.arg('page_size');

-- name: FindUsersBySubject :many
SELECT id, issuer, subject, email
FROM users
WHERE subject = sqlc.arg('subject')::text
ORDER BY id
LIMIT sqlc.arg('page_size');

-- name: ListUserIssuers :many
SELECT DISTINCT issuer FROM users ORDER BY issuer;
