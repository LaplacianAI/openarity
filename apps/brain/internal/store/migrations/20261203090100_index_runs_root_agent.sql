-- +goose NO TRANSACTION
-- +goose Up
-- The watch finds a change's runs by the agents in their trees. Built
-- concurrently, so a large runs table is not locked while it builds; it
-- cannot run in a transaction, hence its own file. A build that fails part
-- way leaves an INVALID index that IF NOT EXISTS would then skip: drop
-- runs_root_agent_idx and migrate again.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_root_agent_idx ON runs (root_run_id, agent_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_root_agent_idx;
