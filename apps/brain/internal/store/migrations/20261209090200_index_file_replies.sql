-- +goose NO TRANSACTION
-- +goose Up
-- One reply for each call that sends a file, so a call made again sends
-- nothing twice; InsertFileReply's ON CONFLICT names it. Concurrently, so the
-- replies table is not locked while it builds; hence its own file. A build
-- that fails part way leaves an INVALID index that IF NOT EXISTS would then
-- skip: drop the index named and migrate again.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS replies_one_per_file_call
    ON replies (run_id, call_id) WHERE kind = 'file';

-- What deleting a file reaches: its cascade to the replies sending it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS replies_attachment_id_idx
    ON replies (attachment_id) WHERE attachment_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS replies_attachment_id_idx;
DROP INDEX CONCURRENTLY IF EXISTS replies_one_per_file_call;
