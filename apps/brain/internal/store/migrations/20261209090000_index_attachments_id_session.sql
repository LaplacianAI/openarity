-- +goose NO TRANSACTION
-- +goose Up
-- A file reply names the attachment it sends and the session it goes to, as a
-- pair, so it cannot send another conversation's file; a foreign key needs a
-- unique index on what it references. Built concurrently, so a large
-- attachments table is not locked while it builds; it cannot run in a
-- transaction, hence its own file. A build that fails part way leaves an
-- INVALID index that IF NOT EXISTS would then skip: drop
-- attachments_id_session_key and migrate again.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS attachments_id_session_key ON attachments (id, session_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS attachments_id_session_key;
