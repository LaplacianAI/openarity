-- +goose Up
-- Proven on the rows already there under a lock that lets writes through.
SET LOCAL lock_timeout = '3s';
ALTER TABLE runs VALIDATE CONSTRAINT runs_waiting_for_known;

-- +goose Down
-- Validating leaves nothing to undo; the constraint goes with the migration before.
SELECT 1;
