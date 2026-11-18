-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A team's sandbox: where its command MCP servers run. A provider, the URL of
-- a self-hosted one, the image rooms start from, and a key, kept as web
-- search's is: sandbox_secret_id names teams/<team_id>/sandbox/<id>, fresh
-- for every key saved, and never the key itself.
ALTER TABLE team_settings
    ADD COLUMN sandbox_provider  text NOT NULL DEFAULT 'none',
    ADD COLUMN sandbox_url       text,
    ADD COLUMN sandbox_secret_id uuid,
    ADD COLUMN sandbox_image     text,
    ADD CONSTRAINT team_settings_sandbox_provider_known CHECK (
        sandbox_provider IN ('none', 'e2b', 'daytona', 'sandboxd')),
    -- E2B is hosted only; Daytona is hosted or self-hosted; sandboxd is self-hosted.
    ADD CONSTRAINT team_settings_sandbox_url_allowed CHECK (
        sandbox_url IS NULL OR sandbox_provider IN ('daytona', 'sandboxd')),
    ADD CONSTRAINT team_settings_sandbox_url_required CHECK (
        sandbox_provider <> 'sandboxd' OR sandbox_url IS NOT NULL),
    ADD CONSTRAINT team_settings_sandbox_secret_iff_provider CHECK (
        (sandbox_secret_id IS NOT NULL) = (sandbox_provider <> 'none')),
    ADD CONSTRAINT team_settings_sandbox_image_allowed CHECK (
        sandbox_image IS NULL OR (sandbox_provider <> 'none' AND sandbox_image <> ''));

-- A command server's plain settings, shown as they are (IMAP_HOST=…), beside
-- env, which holds only references; and where its room may connect.
ALTER TABLE mcp_servers
    ADD COLUMN vars   jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN egress jsonb NOT NULL DEFAULT '[]',
    ADD CONSTRAINT mcp_servers_vars_object CHECK (jsonb_typeof(vars) = 'object'),
    ADD CONSTRAINT mcp_servers_egress_array CHECK (jsonb_typeof(egress) = 'array'),
    ADD CONSTRAINT mcp_servers_room_fields_command_only CHECK (
        command IS NOT NULL OR (vars = '{}' AND egress = '[]'));

-- The replaced and deleted keys are tombstoned as the search key is, by the
-- same triggers, which now name both. The spelling matches secrets.Path; a
-- test asserts it against the Go.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION team_settings_record_replaced_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || o.team_id || '/search/' || o.search_secret_id, o.team_id
    FROM old_rows o JOIN new_rows n USING (team_id)
    WHERE o.search_secret_id IS NOT NULL
      AND o.search_secret_id IS DISTINCT FROM n.search_secret_id
    UNION ALL
    SELECT 'teams/' || o.team_id || '/sandbox/' || o.sandbox_secret_id, o.team_id
    FROM old_rows o JOIN new_rows n USING (team_id)
    WHERE o.sandbox_secret_id IS NOT NULL
      AND o.sandbox_secret_id IS DISTINCT FROM n.sandbox_secret_id
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION team_settings_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/search/' || search_secret_id, team_id
    FROM deleted_rows
    WHERE search_secret_id IS NOT NULL
    UNION ALL
    SELECT 'teams/' || team_id || '/sandbox/' || sandbox_secret_id, team_id
    FROM deleted_rows
    WHERE sandbox_secret_id IS NOT NULL
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '3s';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION team_settings_record_replaced_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || o.team_id || '/search/' || o.search_secret_id, o.team_id
    FROM old_rows o JOIN new_rows n USING (team_id)
    WHERE o.search_secret_id IS NOT NULL
      AND o.search_secret_id IS DISTINCT FROM n.search_secret_id
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION team_settings_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/search/' || search_secret_id, team_id
    FROM deleted_rows
    WHERE search_secret_id IS NOT NULL
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- A key saved since is tombstoned before its column goes, so rolling back
-- never leaves one nothing names.
INSERT INTO deleted_secrets (path, team_id)
SELECT 'teams/' || team_id || '/sandbox/' || sandbox_secret_id, team_id
FROM team_settings WHERE sandbox_secret_id IS NOT NULL
ON CONFLICT (path) DO NOTHING;

ALTER TABLE mcp_servers
    DROP CONSTRAINT mcp_servers_room_fields_command_only,
    DROP COLUMN egress,
    DROP COLUMN vars;

ALTER TABLE team_settings
    DROP COLUMN sandbox_image,
    DROP COLUMN sandbox_secret_id,
    DROP COLUMN sandbox_url,
    DROP COLUMN sandbox_provider;
