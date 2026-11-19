-- +goose Up
SET LOCAL lock_timeout = '3s';

-- Whether an agent may drive a browser: open pages, read them, click and type.
-- Apart from web, which is search and fetch: a browser runs a page's scripts
-- and acts on it, so it is asked for on its own. The hub has it whenever its
-- team has a browser.
ALTER TABLE agents ADD COLUMN browser boolean NOT NULL DEFAULT false;

-- A team's browser: a provider, the URL of a self-hosted one, and a key, kept
-- as web search's is: browser_secret_id names teams/<team_id>/browser/<id>,
-- fresh for every key saved, and never the key itself.
ALTER TABLE team_settings
    ADD COLUMN browser_provider  text NOT NULL DEFAULT 'none',
    ADD COLUMN browser_url       text,
    ADD COLUMN browser_secret_id uuid,
    ADD CONSTRAINT team_settings_browser_provider_known CHECK (
        browser_provider IN ('none', 'browserless', 'steel', 'lightpanda', 'cdp',
                             'browserbase', 'hyperbrowser', 'anchor', 'kernel', 'brightdata')),
    -- Browserless, Steel and Lightpanda are hosted, or self-hosted at a URL;
    -- cdp is any browser at a URL; the rest are hosted only.
    ADD CONSTRAINT team_settings_browser_url_allowed CHECK (
        browser_url IS NULL OR browser_provider IN ('browserless', 'steel', 'lightpanda', 'cdp')),
    ADD CONSTRAINT team_settings_browser_url_required CHECK (
        browser_provider <> 'cdp' OR browser_url IS NOT NULL),
    -- A hosted browser is reached with a key; a self-hosted one may have none.
    ADD CONSTRAINT team_settings_browser_secret_allowed CHECK (
        browser_secret_id IS NULL OR browser_provider <> 'none'),
    ADD CONSTRAINT team_settings_browser_secret_required CHECK (
        browser_secret_id IS NOT NULL OR browser_provider IN ('none', 'cdp') OR browser_url IS NOT NULL);

-- The replaced and deleted keys are tombstoned as the search and sandbox keys
-- are, by the same triggers, which now name all three. The spelling matches
-- secrets.Path; a test asserts it against the Go.
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
    UNION ALL
    SELECT 'teams/' || o.team_id || '/browser/' || o.browser_secret_id, o.team_id
    FROM old_rows o JOIN new_rows n USING (team_id)
    WHERE o.browser_secret_id IS NOT NULL
      AND o.browser_secret_id IS DISTINCT FROM n.browser_secret_id
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
    UNION ALL
    SELECT 'teams/' || team_id || '/browser/' || browser_secret_id, team_id
    FROM deleted_rows
    WHERE browser_secret_id IS NOT NULL
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

-- A key saved since is tombstoned before its column goes, so rolling back
-- never leaves one nothing names.
INSERT INTO deleted_secrets (path, team_id)
SELECT 'teams/' || team_id || '/browser/' || browser_secret_id, team_id
FROM team_settings WHERE browser_secret_id IS NOT NULL
ON CONFLICT (path) DO NOTHING;

ALTER TABLE team_settings
    DROP COLUMN browser_secret_id,
    DROP COLUMN browser_url,
    DROP COLUMN browser_provider;

ALTER TABLE agents DROP COLUMN browser;
