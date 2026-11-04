-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_team_roots_recent_idx
    ON runs (team_id, created_at DESC, id DESC) WHERE id = root_run_id;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_team_roots_recent_idx;
