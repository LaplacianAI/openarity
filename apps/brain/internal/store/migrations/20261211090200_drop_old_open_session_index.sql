-- +goose NO TRANSACTION
-- +goose Up
-- The open conversation of a chat is found by sessions_open_chat_key, which
-- leaves out schedules' and watchers' own sessions; every running brain names
-- that one in EnsureSession's ON CONFLICT since #306. The old index held one
-- open session per (channel, ref) of any kind, so a schedule could not answer
-- in a chat while a person's conversation there was open. Dropped without
-- holding writes.
DROP INDEX CONCURRENTLY IF EXISTS sessions_open_conversation_key;

-- +goose Down
-- Rebuilt without holding writes. It fails, and must, once a schedule shares
-- an open chat with a person's conversation: those two rows are what it would
-- forbid. A failed build leaves an INVALID index; drop it by hand and run
-- again once the sharing sessions are closed.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sessions_open_conversation_key
    ON sessions (channel_id, provider_ref) WHERE status = 'open';
