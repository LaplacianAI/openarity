-- +goose Up
-- Proven on the rows already there under a lock that lets writes through.
SET LOCAL lock_timeout = '3s';
ALTER TABLE watcher_checks VALIDATE CONSTRAINT watcher_checks_outcome_known;
ALTER TABLE watchers VALIDATE CONSTRAINT watchers_source_shape;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
SELECT 1;
