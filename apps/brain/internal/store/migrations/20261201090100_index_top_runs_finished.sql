-- +goose NO TRANSACTION
-- +goose Up
-- The outcome sweep reads finished top-level runs by when they finished.
-- Concurrently, so a large runs table is not locked while it builds; it
-- cannot run in a transaction, hence its own file. A build that fails
-- part way leaves an INVALID index that IF NOT EXISTS would then skip:
-- drop runs_top_finished_idx and migrate again.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_top_finished_idx
    ON runs (finished_at) WHERE parent_run_id IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_top_finished_idx;
