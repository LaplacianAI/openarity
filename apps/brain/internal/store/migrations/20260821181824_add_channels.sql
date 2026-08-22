-- +goose Up
SET lock_timeout = '3s';

CREATE TABLE channels (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id    uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    provider   text NOT NULL,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- provider appears in the hook URL and is matched byte for byte against
    -- the adapter registry, so a stored "Slack" would route nowhere.
    CONSTRAINT channels_provider_lower CHECK (provider = lower(provider)),
    CONSTRAINT channels_provider_present CHECK (provider <> ''),
    CONSTRAINT channels_name_present CHECK (name <> '')
);

-- One team cannot have two channels of the same name. Case-insensitive
-- because the name is how a person refers to a channel on the command line,
-- and "Support" and "support" being different channels is a trap.
CREATE UNIQUE INDEX channels_team_name_key ON channels (team_id, lower(name));

CREATE INDEX channels_team_id_idx ON channels (team_id);

-- +goose Down
DROP TABLE channels;
