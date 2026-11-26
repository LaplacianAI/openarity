-- +goose Up
-- Proven on the rows already there under a lock that lets writes through.
SET LOCAL lock_timeout = '3s';
ALTER TABLE schedules VALIDATE CONSTRAINT schedules_target_known;
ALTER TABLE schedules VALIDATE CONSTRAINT schedules_target_label_bounded;
ALTER TABLE watchers VALIDATE CONSTRAINT watchers_target_known;
ALTER TABLE watchers VALIDATE CONSTRAINT watchers_target_label_bounded;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
SELECT 1;
