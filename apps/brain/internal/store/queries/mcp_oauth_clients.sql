-- name: GetOAuthClient :one
SELECT * FROM mcp_oauth_clients WHERE server_id = $1;

-- ReplaceOAuthClient keeps a registration only over the one the caller read:
-- expected_client_id is that one's client, or null when there was none. Two
-- sign-ins that both registered keep the first; the second gets no row, reads
-- the first's and signs in with it, so a code is never exchanged by a client
-- other than the one it was issued to. The sign-in made with the client
-- replaced ends with it: its tokens are bound to that client, and the
-- tombstone trigger leaves them to the sweep.
-- name: ReplaceOAuthClient :one
WITH saved AS (
    INSERT INTO mcp_oauth_clients AS c (
        server_id, team_id, issuer, redirect_uri, client_id, secret_id, secret_expires_at)
    VALUES (
        sqlc.arg('server_id'), sqlc.arg('team_id'), sqlc.arg('issuer'), sqlc.arg('redirect_uri'),
        sqlc.arg('client_id'), sqlc.narg('secret_id'), sqlc.narg('secret_expires_at'))
    ON CONFLICT (server_id) DO UPDATE SET
        issuer            = EXCLUDED.issuer,
        redirect_uri      = EXCLUDED.redirect_uri,
        client_id         = EXCLUDED.client_id,
        secret_id         = EXCLUDED.secret_id,
        secret_expires_at = EXCLUDED.secret_expires_at,
        registered_at     = now()
    WHERE c.client_id = sqlc.narg('expected_client_id')::text
    RETURNING c.*
), ended AS (
    DELETE FROM mcp_oauth_connections oc USING saved
    WHERE oc.server_id = saved.server_id
)
SELECT * FROM saved;

-- ForgetOAuthClient drops a registration the authorization server no longer
-- accepts, or one a person asked to register again, and the sign-in made with
-- it. Only the client named: one registered since by another sign-in stays.
-- name: ForgetOAuthClient :one
WITH forgotten AS (
    DELETE FROM mcp_oauth_clients mc
    WHERE mc.server_id = sqlc.arg('server_id') AND mc.client_id = sqlc.arg('client_id')
    RETURNING mc.server_id
), ended AS (
    DELETE FROM mcp_oauth_connections oc USING forgotten
    WHERE oc.server_id = forgotten.server_id
)
SELECT count(*)::bigint AS forgotten FROM forgotten;

-- name: ListOAuthClientsOf :many
SELECT * FROM mcp_oauth_clients WHERE server_id = ANY(sqlc.arg('server_ids')::uuid[]);
