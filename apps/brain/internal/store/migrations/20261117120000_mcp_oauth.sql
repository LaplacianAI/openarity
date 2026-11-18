-- +goose Up
SET LOCAL lock_timeout = '3s';

-- How a server is reached: static, with the token in auth_secret_ref or none
-- at all, as every server was until now; or oauth, with a sign-in a person
-- makes on the dashboard. static is the default, so every write that predates
-- signing in keeps meaning what it meant. A server that signs in carries no
-- static token, and the client it signs in as: an id, a secret reference when
-- it is confidential, and the scopes to ask for, which win over the ones
-- discovery finds (Gmail lists full mailbox access among them).
ALTER TABLE mcp_servers
    ADD COLUMN auth_kind text NOT NULL DEFAULT 'static',
    ADD COLUMN oauth_client_id text,
    ADD COLUMN oauth_client_secret_ref text,
    ADD COLUMN oauth_scopes text[];

ALTER TABLE mcp_servers
    ADD CONSTRAINT mcp_servers_auth_kind_known CHECK (auth_kind IN ('static', 'oauth')),
    ADD CONSTRAINT mcp_servers_oauth_has_no_token CHECK (auth_kind = 'static' OR auth_secret_ref IS NULL),
    ADD CONSTRAINT mcp_servers_oauth_fields_for_oauth CHECK (
        auth_kind = 'oauth' OR (oauth_client_id IS NULL AND oauth_client_secret_ref IS NULL AND oauth_scopes IS NULL)),
    ADD CONSTRAINT mcp_servers_oauth_client_present CHECK (oauth_client_id IS NULL OR oauth_client_id <> ''),
    ADD CONSTRAINT mcp_servers_oauth_secret_ref_present CHECK (
        oauth_client_secret_ref IS NULL OR oauth_client_secret_ref <> '');

-- A server's sign-in, once made. The tokens are not here: they are a secret,
-- at teams/<team>/mcp-oauth/<server>. This is what is known about them:
-- where they came from, what they may do, until when, and whether they still
-- work. One per server, for the team; a person's own is a later change.
CREATE TABLE mcp_oauth_connections (
    server_id    uuid PRIMARY KEY,
    team_id      uuid NOT NULL,
    connected_by uuid REFERENCES users (id) ON DELETE SET NULL,

    issuer              text NOT NULL,
    token_endpoint      text NOT NULL,
    revocation_endpoint text,
    resource            text NOT NULL,
    -- The discovery a refresh is made with, as discovery found it.
    auth jsonb NOT NULL,

    scopes_granted    text[] NOT NULL DEFAULT '{}',
    access_expires_at timestamptz,
    status            text NOT NULL DEFAULT 'connected',
    reason            text,

    connected_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT mcp_oauth_connections_server_in_team
        FOREIGN KEY (server_id, team_id) REFERENCES mcp_servers (id, team_id) ON DELETE CASCADE,
    CONSTRAINT mcp_oauth_connections_status_known CHECK (status IN ('connected', 'needs_sign_in')),
    CONSTRAINT mcp_oauth_connections_reason_bounded CHECK (char_length(reason) <= 500)
);

-- A sign-in begun and not yet come back. The callback finds it by its state,
-- which arrives from a browser with no other proof, so the row holds a hash
-- of the state and never the state itself, is taken once, and lasts ten
-- minutes. The PKCE verifier is useless without the code, which only the
-- authorization server and the browser ever see.
CREATE TABLE mcp_oauth_pending (
    state_hash bytea PRIMARY KEY,
    team_id    uuid NOT NULL,
    server_id  uuid NOT NULL,
    started_by uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    verifier     text NOT NULL,
    redirect_uri text NOT NULL,
    auth         jsonb NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,

    CONSTRAINT mcp_oauth_pending_server_in_team
        FOREIGN KEY (server_id, team_id) REFERENCES mcp_servers (id, team_id) ON DELETE CASCADE,
    CONSTRAINT mcp_oauth_pending_state_is_a_sha256 CHECK (octet_length(state_hash) = 32)
);

CREATE INDEX mcp_oauth_pending_expiry_idx ON mcp_oauth_pending (expires_at);

-- A connection gone, by a disconnect or with its server or team, leaves its
-- tokens to delete; the sweep that deletes a channel's secret does it. The
-- path is built here as secrets.Path builds it, and a test holds the two to
-- each other.
-- +goose StatementBegin
CREATE FUNCTION mcp_oauth_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/mcp-oauth/' || server_id, team_id FROM deleted_rows
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER mcp_oauth_tombstone_deleted_secrets
    AFTER DELETE ON mcp_oauth_connections
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION mcp_oauth_record_deleted_secret();

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TRIGGER mcp_oauth_tombstone_deleted_secrets ON mcp_oauth_connections;
DROP FUNCTION mcp_oauth_record_deleted_secret();
DROP TABLE mcp_oauth_pending;
DROP TABLE mcp_oauth_connections;

-- A server that signed in has no static token to go back to; it becomes one
-- with no auth, and its client is dropped with the columns.
ALTER TABLE mcp_servers
    DROP CONSTRAINT mcp_servers_oauth_secret_ref_present,
    DROP CONSTRAINT mcp_servers_oauth_client_present,
    DROP CONSTRAINT mcp_servers_oauth_fields_for_oauth,
    DROP CONSTRAINT mcp_servers_oauth_has_no_token,
    DROP CONSTRAINT mcp_servers_auth_kind_known,
    DROP COLUMN oauth_scopes,
    DROP COLUMN oauth_client_secret_ref,
    DROP COLUMN oauth_client_id,
    DROP COLUMN auth_kind;
