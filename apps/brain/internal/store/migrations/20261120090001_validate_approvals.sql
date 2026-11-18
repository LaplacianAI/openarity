-- +goose Up
-- VALIDATE scans without an exclusive lock; the checks were added NOT VALID
-- in 20261120090000_approvals.sql so that migration held its locks briefly.
-- run_events_kind_known stays NOT VALID, as every replacement of it has.
ALTER TABLE runs VALIDATE CONSTRAINT runs_status_known;
ALTER TABLE runs VALIDATE CONSTRAINT runs_waiting_shape;
ALTER TABLE runs VALIDATE CONSTRAINT runs_attempt_nonneg;
ALTER TABLE team_settings VALIDATE CONSTRAINT team_settings_approval_mode_known;
ALTER TABLE team_settings VALIDATE CONSTRAINT team_settings_approval_ttl_range;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
