-- +goose Up
ALTER TABLE agents VALIDATE CONSTRAINT agents_delegation_known;

-- +goose Down
-- Validating leaves nothing to undo; the constraint goes with the migration before.
