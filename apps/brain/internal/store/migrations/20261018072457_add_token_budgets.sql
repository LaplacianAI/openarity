-- +goose Up
SET lock_timeout = '3s';

-- What one run of the agent may spend, input and output tokens together.
-- NULL is the deployment's default.
ALTER TABLE agents ADD COLUMN token_budget bigint;
ALTER TABLE agents ADD CONSTRAINT agents_token_budget_positive
    CHECK (token_budget > 0) NOT VALID;

-- Copied from the agent when the run is created, so editing the agent does
-- not move the limit of a run already going, and a run taken over keeps the
-- one it started with. NULL is the deployment's default.
ALTER TABLE runs ADD COLUMN token_budget bigint;
ALTER TABLE runs ADD CONSTRAINT runs_token_budget_positive
    CHECK (token_budget > 0) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

ALTER TABLE runs DROP COLUMN token_budget;
ALTER TABLE agents DROP COLUMN token_budget;
