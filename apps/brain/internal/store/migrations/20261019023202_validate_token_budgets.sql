-- +goose Up
ALTER TABLE agents VALIDATE CONSTRAINT agents_token_budget_positive;
ALTER TABLE runs VALIDATE CONSTRAINT runs_token_budget_positive;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
