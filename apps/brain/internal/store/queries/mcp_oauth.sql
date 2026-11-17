-- SetMCPServerOAuth makes a server one that signs in, with the client it
-- signs in as. Its static token, if it had one, is dropped: a server is
-- reached one way. A sign-in it already has is left; one made with another
-- client is the caller's to end.
-- name: SetMCPServerOAuth :one
UPDATE mcp_servers SET
    auth_kind               = 'oauth',
    auth_secret_ref         = NULL,
    oauth_client_id         = sqlc.narg('client_id'),
    oauth_client_secret_ref = sqlc.narg('client_secret_ref'),
    oauth_scopes            = sqlc.narg('scopes')::text[],
    updated_at              = now()
WHERE id = sqlc.arg('id') AND team_id = sqlc.arg('team_id')
RETURNING *;

-- name: InsertOAuthPending :exec
INSERT INTO mcp_oauth_pending (state_hash, team_id, server_id, started_by, verifier, redirect_uri, auth, expires_at)
VALUES (sqlc.arg('state_hash'), sqlc.arg('team_id'), sqlc.arg('server_id'), sqlc.arg('started_by'),
        sqlc.arg('verifier'), sqlc.arg('redirect_uri'), sqlc.arg('auth'), sqlc.arg('expires_at'));

-- TakeOAuthPending is the callback's one look at a sign-in begun: deleted as
-- it is read, so a state works once, and only while it lasts. An expired one
-- is left for the sweep rather than taken, so a late callback changes
-- nothing either way.
-- name: TakeOAuthPending :one
DELETE FROM mcp_oauth_pending
WHERE state_hash = sqlc.arg('state_hash') AND expires_at > now()
RETURNING *;

-- name: PruneOAuthPending :execrows
DELETE FROM mcp_oauth_pending WHERE expires_at <= now();

-- UpsertOAuthConnection records a sign-in made: anew, or again in place of
-- one that needed it.
-- name: UpsertOAuthConnection :one
INSERT INTO mcp_oauth_connections (
    server_id, team_id, connected_by, issuer, token_endpoint, revocation_endpoint, resource, auth,
    scopes_granted, access_expires_at, status, reason)
VALUES (
    sqlc.arg('server_id'), sqlc.arg('team_id'), sqlc.narg('connected_by'), sqlc.arg('issuer'),
    sqlc.arg('token_endpoint'), sqlc.narg('revocation_endpoint'), sqlc.arg('resource'), sqlc.arg('auth'),
    sqlc.arg('scopes_granted')::text[], sqlc.narg('access_expires_at'), 'connected', NULL)
ON CONFLICT (server_id) DO UPDATE SET
    connected_by        = EXCLUDED.connected_by,
    issuer              = EXCLUDED.issuer,
    token_endpoint      = EXCLUDED.token_endpoint,
    revocation_endpoint = EXCLUDED.revocation_endpoint,
    resource            = EXCLUDED.resource,
    auth                = EXCLUDED.auth,
    scopes_granted      = EXCLUDED.scopes_granted,
    access_expires_at   = EXCLUDED.access_expires_at,
    status              = 'connected',
    reason              = NULL,
    connected_at        = now(),
    updated_at          = now()
RETURNING *;

-- name: GetOAuthConnection :one
SELECT * FROM mcp_oauth_connections WHERE server_id = $1;
