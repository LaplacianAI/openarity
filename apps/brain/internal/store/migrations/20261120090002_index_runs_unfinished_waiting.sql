-- +goose NO TRANSACTION
-- +goose Up
-- A run waiting for an approval is unfinished too. A partial index serves a
-- query only when the query's predicate implies the index's, so a "not
-- finished" query that names waiting can no longer use runs_unfinished_idx.
-- Built beside it and swapped, CONCURRENTLY, so writes are never blocked.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_active_idx ON runs (status, heartbeat_at)
    WHERE status IN ('queued', 'running', 'waiting');
DROP INDEX CONCURRENTLY IF EXISTS runs_unfinished_idx;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_unfinished_idx ON runs (status, heartbeat_at)
    WHERE status IN ('queued', 'running');
DROP INDEX CONCURRENTLY IF EXISTS runs_active_idx;
