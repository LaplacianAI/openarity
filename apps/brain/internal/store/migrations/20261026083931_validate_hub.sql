-- +goose Up
ALTER TABLE runs VALIDATE CONSTRAINT runs_hub_is_a_root;
ALTER TABLE run_events VALIDATE CONSTRAINT run_events_kind_known;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
