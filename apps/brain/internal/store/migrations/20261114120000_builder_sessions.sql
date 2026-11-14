-- +goose Up
SET lock_timeout = '3s';

-- The builder is an agent on the dashboard channel: a conversation with it is
-- a dashboard conversation like any other, and every message in it has its
-- channel. builder says the builder answers it rather than the hub. A session
-- is one or the other from the start, and never changes.
ALTER TABLE sessions ADD COLUMN builder boolean NOT NULL DEFAULT false;

-- Builder conversations started before this had no channel. Each moves onto
-- its team's dashboard channel, named as a dashboard conversation is: by the
-- person it is with, and something of its own. Only a session every top-level
-- run of which is the builder's moves; one that also held another agent's runs
-- stays where it is.
UPDATE sessions s
SET builder = true,
    channel_id = c.id,
    provider_ref = s.user_id::text || ':builder-' || s.id::text
FROM channels c
WHERE c.team_id = s.team_id
  AND c.provider = 'dashboard'
  AND s.channel_id IS NULL
  AND s.user_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM runs r WHERE r.session_id = s.id AND r.parent_run_id IS NULL AND r.builder)
  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.session_id = s.id AND r.parent_run_id IS NULL AND NOT r.builder);

ALTER TABLE sessions ADD CONSTRAINT sessions_builder_on_a_channel
    CHECK (NOT builder OR channel_id IS NOT NULL);

-- +goose Down
SET lock_timeout = '3s';

-- The moved conversations stay on the dashboard channel, where they may now
-- hold messages; taking them off it would leave those messages in a session
-- with no channel. They read as dashboard conversations the hub answers.
ALTER TABLE sessions DROP CONSTRAINT sessions_builder_on_a_channel;
ALTER TABLE sessions DROP COLUMN builder;
