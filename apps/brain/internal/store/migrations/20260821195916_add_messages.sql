-- +goose Up
SET lock_timeout = '3s';

-- messages is a stand-in for the orchestrator, which owns conversation state
-- once it exists. It holds the least a reader needs to see that the path works
-- end to end, and deliberately not the whole of Inbound: mentions, attachments
-- and enrichment belong to whatever renders a prompt, not to a table the
-- gateway writes.
--
-- Every row here belongs to an approved sender. An unapproved one never gets
-- this far, so no message body from a stranger exists anywhere in the database.
CREATE TABLE messages (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id       uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    external_id      text NOT NULL,
    conversation_ref text NOT NULL,
    text             text NOT NULL,

    -- Null when the provider did not say. It is the sender's clock either
    -- way, so nothing may order on it: received_at is ours.
    sent_at     timestamptz,
    received_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT messages_external_id_present CHECK (external_id <> ''),
    CONSTRAINT messages_conversation_ref_present CHECK (conversation_ref <> '')
);

-- Every provider retries whenever a response is slow, so a duplicate is the
-- normal case rather than an edge one. The database absorbs it, which is what
-- lets the handler answer 200 without asking first.
--
-- Scoped to the channel because two channels can legitimately see the same
-- provider-side id: Slack's ts repeats across workspaces.
CREATE UNIQUE INDEX messages_channel_external_key ON messages (channel_id, external_id);

-- Reading a channel's inbox newest first, which is the only query there is.
CREATE INDEX messages_channel_received_idx ON messages (channel_id, received_at DESC);

-- +goose Down
DROP TABLE messages;
