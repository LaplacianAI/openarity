-- name: UpsertUser :one
INSERT INTO users (issuer, subject, email)
VALUES ($1, $2, $3)
ON CONFLICT (issuer, subject) DO UPDATE
SET email      = EXCLUDED.email,
    updated_at = now()
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;
