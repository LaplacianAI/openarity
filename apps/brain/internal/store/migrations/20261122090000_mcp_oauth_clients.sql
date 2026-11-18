-- +goose Up
SET LOCAL lock_timeout = '3s';

-- The OAuth client the brain registered for a server that signs in, when the
-- server has none of its own: RFC 7591, made at the first Connect and kept so
-- every later sign-in, refresh and revocation uses the same one. A client set
-- on the server itself (mcp_servers.oauth_client_id) always wins over this.
--
-- A registration holds while the authorization server and the redirect URI
-- it was made with are the ones a sign-in would use, and its secret has not
-- expired: past any of those it is replaced, never patched.
--
-- Most registrations are public clients with no secret. When the
-- authorization server issues one anyway, it is never here: secret_id names
-- the secret-store path teams/<team_id>/mcp-oauth-client/<secret_id>, fresh
-- for every registration, so a replaced one is tombstoned without risk of
-- destroying its successor.
CREATE TABLE mcp_oauth_clients (
    server_id uuid PRIMARY KEY,
    team_id   uuid NOT NULL,

    issuer       text NOT NULL,
    redirect_uri text NOT NULL,
    client_id    text NOT NULL,

    secret_id         uuid,
    secret_expires_at timestamptz,

    registered_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT mcp_oauth_clients_server_in_team
        FOREIGN KEY (server_id, team_id) REFERENCES mcp_servers (id, team_id) ON DELETE CASCADE,
    CONSTRAINT mcp_oauth_clients_client_present CHECK (client_id <> '' AND char_length(client_id) <= 2048),
    CONSTRAINT mcp_oauth_clients_issuer_present CHECK (issuer <> ''),
    CONSTRAINT mcp_oauth_clients_redirect_present CHECK (redirect_uri <> ''),
    CONSTRAINT mcp_oauth_clients_expiry_with_secret CHECK (secret_expires_at IS NULL OR secret_id IS NOT NULL)
);

-- A registration replaced or deleted leaves its secret to the sweep. A trigger
-- rather than the handler, so a replace that races another, or a server
-- deleted with its team, cannot leave a secret nothing names. The spelling
-- matches secrets.Path exactly; a test asserts it against the Go.
-- +goose StatementBegin
CREATE FUNCTION mcp_oauth_clients_record_replaced_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || o.team_id || '/mcp-oauth-client/' || o.secret_id, o.team_id
    FROM old_rows o JOIN new_rows n USING (server_id)
    WHERE o.secret_id IS NOT NULL
      AND o.secret_id IS DISTINCT FROM n.secret_id
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION mcp_oauth_clients_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/mcp-oauth-client/' || secret_id, team_id
    FROM deleted_rows
    WHERE secret_id IS NOT NULL
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Two triggers because a trigger with transition tables may name one event.
-- An upsert that takes the conflict path fires the UPDATE one.
CREATE TRIGGER mcp_oauth_clients_tombstone_replaced_secret
    AFTER UPDATE ON mcp_oauth_clients
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION mcp_oauth_clients_record_replaced_secret();

-- Deleting a server or its team cascades here without running any Go.
CREATE TRIGGER mcp_oauth_clients_tombstone_deleted_secret
    AFTER DELETE ON mcp_oauth_clients
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION mcp_oauth_clients_record_deleted_secret();

-- +goose Down
SET LOCAL lock_timeout = '3s';

-- A server that signed in with a registered client keeps its row and loses
-- the client: its next Connect is refused until a client is set on it.
DROP TRIGGER mcp_oauth_clients_tombstone_deleted_secret ON mcp_oauth_clients;
DROP TRIGGER mcp_oauth_clients_tombstone_replaced_secret ON mcp_oauth_clients;
DROP FUNCTION mcp_oauth_clients_record_deleted_secret();
DROP FUNCTION mcp_oauth_clients_record_replaced_secret();
DROP TABLE mcp_oauth_clients;
