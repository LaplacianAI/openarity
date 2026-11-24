-- +goose NO TRANSACTION
-- +goose Up
-- A run's files are attachments of its session, and they name the pair so
-- one cannot name another session's run; a foreign key needs a unique index
-- on what it references. Built concurrently, so a large runs table is not
-- locked while it builds; it cannot run in a transaction, hence its own file.
-- A build that fails part way leaves an INVALID index that IF NOT EXISTS
-- would then skip: drop runs_id_session_key and migrate again.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runs_id_session_key ON runs (id, session_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS runs_id_session_key;
