-- +goose NO TRANSACTION
-- +goose Up
-- The outcome sweep reads finished top-level runs by when they finished.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_top_finished_idx
    ON runs (finished_at) WHERE parent_run_id IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_top_finished_idx;
