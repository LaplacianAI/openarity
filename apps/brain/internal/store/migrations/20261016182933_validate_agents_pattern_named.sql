-- +goose Up
ALTER TABLE agents VALIDATE CONSTRAINT agents_pattern_named;

-- +goose Down
-- Validating leaves nothing to undo; the constraint goes with the migration before.
