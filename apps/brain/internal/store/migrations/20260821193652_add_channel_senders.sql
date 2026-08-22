-- +goose Up
SET lock_timeout = '3s';

-- A sender is keyed on the channel, never on the provider: Slack user ids are
-- unique only within a workspace, and "agent-17" in a partner's channel has
-- nothing to do with "agent-17" in ours. Keying on the ref alone would let an
-- approval in a channel you trust silently authorise a stranger in one you do
-- not.
CREATE TABLE channel_senders (
    channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_ref text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, sender_ref),
    CONSTRAINT channel_senders_ref_present CHECK (sender_ref <> '')
);

-- Finding every channel a person speaks in, for a delete or an audit.
CREATE INDEX channel_senders_user_id_idx ON channel_senders (user_id);

-- pending_senders is written by an unauthenticated request from the open
-- internet: anyone who finds the hook URL and holds the signing secret can
-- create a row. So it holds no message body — only who claimed to speak.
-- Storing the text would let a stranger write arbitrary content into a table
-- an admin later reads.
CREATE TABLE pending_senders (
    channel_id  uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_ref  text NOT NULL,
    sender_name text NOT NULL DEFAULT '',
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    seen_count  integer NOT NULL DEFAULT 1,

    PRIMARY KEY (channel_id, sender_ref),
    CONSTRAINT pending_senders_ref_present CHECK (sender_ref <> ''),
    -- The name is clipped in Go before it arrives. This is the backstop for
    -- anything that reaches the table another way, so an admin's terminal
    -- cannot be flooded by one row.
    CONSTRAINT pending_senders_name_bounded CHECK (char_length(sender_name) <= 64)
);

-- +goose Down
DROP TABLE pending_senders;
DROP TABLE channel_senders;
