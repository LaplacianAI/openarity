-- +goose Up
SET lock_timeout = '3s';

-- A WhatsApp channel's linked device, and who holds its connection. A
-- connection has one owner: a `brain whatsapp` role leases the row, renews
-- it every 15 seconds, and loses it after 60, as a run's worker does.
-- Pairing goes through the row too: a request bumps pair_seq, the holder
-- starts it and writes the newest QR or code, then the number it linked.
CREATE TABLE whatsapp_accounts (
    channel_id   uuid PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    state        text NOT NULL DEFAULT 'unpaired',
    -- The linked device, once paired: what the device store knows it by.
    jid          text,
    phone        text,
    -- pair_seq is bumped by each request to pair; pair_seen is the request
    -- the holder last took, so a role that restarts does not take it again.
    pair_seq     bigint NOT NULL DEFAULT 0,
    pair_seen    bigint NOT NULL DEFAULT 0,
    pair_method  text,
    pair_phone   text,
    -- The newest QR's text, which rotates; cleared once linked.
    pair_qr      text,
    pair_code    text,
    pair_error   text,
    held_by      text,
    heartbeat_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT whatsapp_accounts_state_known CHECK (state IN ('unpaired', 'pairing', 'connected')),
    CONSTRAINT whatsapp_accounts_pair_method_known CHECK (pair_method IN ('qr', 'code')),
    CONSTRAINT whatsapp_accounts_code_has_phone CHECK (pair_method IS DISTINCT FROM 'code' OR pair_phone IS NOT NULL),
    CONSTRAINT whatsapp_accounts_connected_has_jid CHECK (state <> 'connected' OR jid IS NOT NULL)
);

-- Each part already sent, so a retry after a lost answer sends no second
-- message. Kept a day: a reply is given up long before.
CREATE TABLE whatsapp_sent (
    channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    part_id    text NOT NULL,
    message_id text NOT NULL,
    sent_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, part_id)
);
CREATE INDEX whatsapp_sent_sent_at_idx ON whatsapp_sent (sent_at);

-- whatsmeow's device store lives here, under its own migrations, which the
-- role runs on start. It holds each linked device's keys: whoever reads this
-- schema can act as the number.
CREATE SCHEMA IF NOT EXISTS whatsmeow;

-- +goose Down
SET lock_timeout = '3s';

-- Rolling back unlinks nothing on the phones: the devices' keys go, and each
-- phone shows a device that never connects again until it is removed there.
DROP SCHEMA IF EXISTS whatsmeow CASCADE;
DROP TABLE whatsapp_sent;
DROP TABLE whatsapp_accounts;
