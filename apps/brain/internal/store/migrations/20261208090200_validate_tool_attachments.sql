-- +goose NO TRANSACTION
-- +goose Up
-- Validated apart from where they were added, so neither table is held under
-- the lock the add takes while every row is read; the index is built
-- concurrently for the same reason. A failed build leaves an INVALID index
-- that IF NOT EXISTS would skip: drop attachments_run_id_idx and migrate again.
ALTER TABLE attachments VALIDATE CONSTRAINT attachments_run_in_session;
ALTER TABLE attachments VALIDATE CONSTRAINT attachments_one_source;
ALTER TABLE attachments VALIDATE CONSTRAINT attachments_tool_bounded;
ALTER TABLE attachments VALIDATE CONSTRAINT attachments_call_id_bounded;

-- A run's files, for what deleting or reading a run reaches.
CREATE INDEX CONCURRENTLY IF NOT EXISTS attachments_run_id_idx ON attachments (run_id) WHERE run_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS attachments_run_id_idx;
-- Validating leaves nothing else to undo; the constraints go with the migration before.
