-- +goose Up
SET lock_timeout = '3s';

-- A session is the thing an agent works inside: one conversation, and later
-- the workspace, sandbox and files that belong to it. Everything else points
-- at it, so it is a first-class row with a stable id rather than a grouping
-- derived from messages.
--
-- It belongs to a team, not to a channel. A channel is one way a session
-- starts — the dashboard and the API are others, and neither has a webhook
-- behind it. Making channel_id optional now is what stops "start a session
-- without a channel" being a migration later.
--
-- provider_ref is the adapter's answer to "which conversation is this", and it
-- is the whole of session identity for a channel session. What goes in it
-- differs by platform and is the adapter's decision, never the gateway's:
--
--   custom    whatever the downstream service sends; it knows its own episodes
--   Slack     C123:1699999999.000100 in a thread, D01ABC in a direct message
--   Telegram  the chat id
--   WhatsApp  the phone number, because there is nothing else
CREATE TABLE sessions (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,

    channel_id   uuid,
    provider_ref text,

    -- How many people can speak, not what the provider calls the room.
    -- Slack's mpim looks like a direct message and is a group.
    kind text NOT NULL,

    -- Unused today: there is exactly one open session per conversation and
    -- nothing closes one. They are here because only a clock can say where a
    -- conversation ended, and most platforms need that — a Slack thread ends
    -- on its own, a Slack direct message never does, and neither does
    -- WhatsApp. Carrying the columns now makes an idle sweep an UPDATE rather
    -- than a backfill of every message.
    status          text NOT NULL DEFAULT 'open',
    started_at      timestamptz NOT NULL DEFAULT now(),
    last_message_at timestamptz NOT NULL DEFAULT now(),

    -- Either it came from a channel and has both, or it did not and has
    -- neither. A ref with no channel names nothing.
    CONSTRAINT sessions_channel_and_ref_together
        CHECK ((channel_id IS NULL) = (provider_ref IS NULL)),

    CONSTRAINT sessions_ref_present CHECK (provider_ref <> ''),

    -- The same bound the gateway refuses at, for the same reason: this arrives
    -- from an unauthenticated webhook and is stored exactly as sent.
    CONSTRAINT sessions_ref_bounded CHECK (char_length(provider_ref) <= 256),

    CONSTRAINT sessions_kind_known CHECK (kind IN ('direct', 'group', 'thread')),
    CONSTRAINT sessions_status_known CHECK (status IN ('open', 'closed'))
);

-- A session carries its own team so that authorisation never depends on a
-- channel that may not exist. That leaves two copies of the same fact whenever
-- there is a channel, so the database keeps them equal rather than trusting
-- every writer to: the composite foreign key below cannot be satisfied unless
-- the channel really is in that team.
ALTER TABLE channels ADD CONSTRAINT channels_id_team_key UNIQUE (id, team_id);

ALTER TABLE sessions
    ADD CONSTRAINT sessions_channel_in_team
        FOREIGN KEY (channel_id, team_id) REFERENCES channels (id, team_id) ON DELETE CASCADE;

-- Partial, and that is the point. Today it enforces one session per
-- conversation, so find-or-create is unambiguous. The day a sweep closes a
-- stale session, the next message opens a second row for the same
-- provider_ref with no migration and no ambiguity about which one is current.
CREATE UNIQUE INDEX sessions_open_conversation_key
    ON sessions (channel_id, provider_ref) WHERE status = 'open';

-- Listing a team's or a channel's sessions, most recently spoken in first.
CREATE INDEX sessions_team_recent_idx ON sessions (team_id, last_message_at DESC);
CREATE INDEX sessions_channel_recent_idx ON sessions (channel_id, last_message_at DESC);

-- A message belongs to a session rather than carrying a loose conversation
-- reference. Nothing is deployed and no row exists, so this drops the column
-- rather than backfilling it.
ALTER TABLE messages
    DROP CONSTRAINT messages_conversation_ref_present,
    DROP COLUMN conversation_ref,
    ADD COLUMN session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE;

-- Reading a session's messages, which is now the only message query there is.
CREATE INDEX messages_session_received_idx ON messages (session_id, received_at DESC);

-- +goose Down
DROP INDEX messages_session_received_idx;

ALTER TABLE messages
    DROP COLUMN session_id,
    ADD COLUMN conversation_ref text NOT NULL DEFAULT '';

ALTER TABLE messages ALTER COLUMN conversation_ref DROP DEFAULT;
ALTER TABLE messages
    ADD CONSTRAINT messages_conversation_ref_present CHECK (conversation_ref <> '');

DROP TABLE sessions;
ALTER TABLE channels DROP CONSTRAINT channels_id_team_key;
