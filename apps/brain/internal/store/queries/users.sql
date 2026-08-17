-- name: UpsertUser :one
INSERT INTO users (issuer, subject, email)
VALUES ($1, $2, $3)
ON CONFLICT (issuer, subject) DO UPDATE
SET email      = EXCLUDED.email,
    updated_at = now()
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
