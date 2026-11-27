-- +goose Up
-- A held delivery keeps the event its sender named (GitHub's
-- X-GitHub-Event), so the run that takes it in later still says what it was.
-- No webhook watcher could be made before this, so the table is empty and the
-- check scans nothing.
SET LOCAL lock_timeout = '3s';
ALTER TABLE watcher_deliveries
    ADD COLUMN event text,
    ADD CONSTRAINT watcher_deliveries_event_bounded CHECK (length(event) BETWEEN 1 AND 100);

-- +goose Down
SET LOCAL lock_timeout = '3s';
ALTER TABLE watcher_deliveries DROP COLUMN event;
