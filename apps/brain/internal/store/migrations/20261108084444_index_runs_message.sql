-- One run per message: a webhook the provider retries finds the run its first
-- delivery started and starts nothing. Concurrently, because runs takes writes
-- all the time and a plain CREATE INDEX would block them for the whole build.

-- +goose NO TRANSACTION
-- +goose Up
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runs_message_key
    ON runs (message_id) WHERE message_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_message_key;
