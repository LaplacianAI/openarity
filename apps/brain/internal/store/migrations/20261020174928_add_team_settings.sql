-- +goose Up
SET lock_timeout = '3s';

-- A team's settings: the model its built-in agents run on, its web search
-- provider, whether the learner runs, and when a transcript compacts. No row
-- until the first save; the API reads the absence as the defaults, so
-- creating a team writes nothing here and no existing team needs a backfill.
--
-- The search key is never here. search_secret_id names the secret-store path
-- teams/<team_id>/search/<id>, fresh for every key saved.
CREATE TABLE team_settings (
    team_id              uuid PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,

    model_name           text,
    model_max_tokens     integer,
    model_temperature    double precision,


    search_provider      text NOT NULL DEFAULT 'none',
    search_url           text,
    search_secret_id     uuid,

    learning             boolean NOT NULL DEFAULT false,
    compaction_messages  integer NOT NULL DEFAULT 100,

    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT team_settings_model_complete CHECK (
        (model_name IS NULL AND model_max_tokens IS NULL AND model_temperature IS NULL)
        OR (model_name IS NOT NULL AND model_max_tokens IS NOT NULL)),
    CONSTRAINT team_settings_model_max_tokens_positive CHECK (model_max_tokens > 0),
    CONSTRAINT team_settings_model_temperature_range CHECK (model_temperature BETWEEN 0 AND 2),
    CONSTRAINT team_settings_search_provider_known CHECK (
        search_provider IN ('none', 'searxng', 'brave', 'tavily',
                            'firecrawl-cloud', 'firecrawl-oss')),
    CONSTRAINT team_settings_search_url_iff_self_hosted CHECK (
        (search_url IS NOT NULL) = (search_provider IN ('searxng', 'firecrawl-oss'))),
    CONSTRAINT team_settings_search_secret_required CHECK (
        search_secret_id IS NOT NULL
        OR search_provider NOT IN ('brave', 'tavily', 'firecrawl-cloud')),
    CONSTRAINT team_settings_search_secret_allowed CHECK (
        search_secret_id IS NULL
        OR search_provider IN ('brave', 'tavily', 'firecrawl-cloud', 'firecrawl-oss')),
    CONSTRAINT team_settings_compaction_range CHECK (compaction_messages BETWEEN 10 AND 10000)
);

-- A replaced key is tombstoned in the transaction that replaced it, and
-- brain reap destroys it. A trigger rather than the handler, so a save that
-- races another, or a row changed by anything but the API, cannot leave a
-- key nothing names. The spelling matches secrets.Path exactly; a test
-- asserts it against the Go.
-- +goose StatementBegin
CREATE FUNCTION team_settings_record_replaced_secret() RETURNS trigger
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
CREATE FUNCTION team_settings_record_deleted_secret() RETURNS trigger
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

-- Two triggers because a trigger with transition tables may name one event.
-- An upsert that takes the conflict path fires the UPDATE one.
CREATE TRIGGER team_settings_tombstone_replaced_secret
    AFTER UPDATE ON team_settings
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION team_settings_record_replaced_secret();

-- Deleting a team cascades here without running any Go.
CREATE TRIGGER team_settings_tombstone_deleted_secret
    AFTER DELETE ON team_settings
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION team_settings_record_deleted_secret();

-- +goose Down
DROP TRIGGER team_settings_tombstone_deleted_secret ON team_settings;
DROP TRIGGER team_settings_tombstone_replaced_secret ON team_settings;
DROP FUNCTION team_settings_record_deleted_secret();
DROP FUNCTION team_settings_record_replaced_secret();
DROP TABLE team_settings;
