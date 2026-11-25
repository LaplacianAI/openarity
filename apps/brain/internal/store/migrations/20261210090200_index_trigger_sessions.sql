-- +goose NO TRANSACTION
-- +goose Up
-- Built without holding writes, so a failed build leaves an INVALID index
-- behind: IF NOT EXISTS lets the retry go on after it is dropped by hand.
--
-- One conversation per schedule or watcher.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sessions_schedule_key ON sessions (schedule_id)
    WHERE schedule_id IS NOT NULL;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sessions_watcher_key ON sessions (watcher_id)
    WHERE watcher_id IS NOT NULL;

-- The open conversation of a chat, which a message arriving finds, is never
-- a schedule's or a watcher's: theirs share the chat's channel and ref, so
-- their answers post there, but a person writing in the chat is in their own.
-- The old index stays until no running brain names it in EnsureSession's
-- ON CONFLICT; a later migration drops it.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sessions_open_chat_key ON sessions (channel_id, provider_ref)
    WHERE status = 'open' AND schedule_id IS NULL AND watcher_id IS NULL;

-- A Slack post found by where it landed, for a reply in its thread.
CREATE INDEX CONCURRENTLY IF NOT EXISTS replies_posted_ref ON replies (channel_id, posted_ref)
    WHERE posted_ref IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS replies_posted_ref;
DROP INDEX CONCURRENTLY IF EXISTS sessions_open_chat_key;
DROP INDEX CONCURRENTLY IF EXISTS sessions_watcher_key;
DROP INDEX CONCURRENTLY IF EXISTS sessions_schedule_key;
