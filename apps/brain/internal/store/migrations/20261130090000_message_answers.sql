-- +goose Up
SET LOCAL lock_timeout = '3s';
-- An answer given with a platform's own control (a tap, a button, a poll
-- vote) that decided a request, or was told its request was already over. It
-- is the record of an action, not something said: the conversation, the next
-- run's input and the session's reads leave it out. A constant default, so
-- adding it rewrites nothing.
ALTER TABLE messages ADD COLUMN answer boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN messages.answer IS
    'An answer to a request (a tap, a vote, a typed code), not something said: every read of the conversation must leave it out.';

-- +goose Down
SET LOCAL lock_timeout = '3s';
ALTER TABLE messages DROP COLUMN answer;
