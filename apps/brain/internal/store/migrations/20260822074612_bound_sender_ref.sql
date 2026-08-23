-- +goose Up
SET lock_timeout = '3s';

-- sender_name was bounded when these tables were created; sender_ref was not,
-- and it is the more exposed of the two. Both arrive from an unauthenticated
-- webhook, but the name is clipped by CleanName on the way in while the ref is
-- stored exactly as sent, because it is an identity and clipping two distinct
-- refs to the same value would merge two people.
--
-- So the ref is bounded by refusal rather than by truncation: the gateway drops
-- a message whose ref is longer than this, and these constraints are the
-- backstop for anything that reaches the table another way. Without them a body
-- at the 1 MiB limit is a 1 MiB ref, fifty of them per channel, written by
-- anyone who knows the URL.
--
-- 256 is far beyond any real provider. Slack's are eleven characters, Telegram
-- sends a number, and custom composes at most a conversation and a timestamp.
ALTER TABLE channel_senders
    ADD CONSTRAINT channel_senders_ref_bounded CHECK (char_length(sender_ref) <= 256);

ALTER TABLE pending_senders
    ADD CONSTRAINT pending_senders_ref_bounded CHECK (char_length(sender_ref) <= 256);

-- +goose Down
ALTER TABLE channel_senders DROP CONSTRAINT channel_senders_ref_bounded;
ALTER TABLE pending_senders DROP CONSTRAINT pending_senders_ref_bounded;
