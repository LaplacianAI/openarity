-- +goose Up
SET LOCAL lock_timeout = '3s';

-- Who wrote a message, as the platform named them, so a conversation read
-- back has speakers: a channel sender has no email, and was "someone". The
-- name is cleaned and clipped in Go (gateway.CleanName); this is the backstop.
-- It is attacker-controlled on every platform, and read as data. Rows from
-- before keep an empty name, and fall back to the email.
ALTER TABLE messages
    ADD COLUMN sender_name text NOT NULL DEFAULT '',
    ADD CONSTRAINT messages_sender_name_bounded CHECK (char_length(sender_name) <= 64);

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE messages DROP COLUMN sender_name;
