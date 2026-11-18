-- +goose Up
SET LOCAL lock_timeout = '3s';

-- The word a message must hold to ask a WhatsApp channel, such as @Fduv:
-- for a number that is also a person's own, so a friend tagging them is not
-- answered by the bot in their name, and they can call it from any chat.
-- Null is no word: direct chats, and groups where the number is mentioned.
ALTER TABLE channels
    ADD COLUMN trigger_word text,
    ADD CONSTRAINT channels_trigger_word_length CHECK (char_length(trigger_word) BETWEEN 1 AND 32);

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE channels DROP COLUMN trigger_word;
