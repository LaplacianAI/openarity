-- +goose Up
SET LOCAL lock_timeout = '3s';

-- How much of a run's input the provider served from its cache. It is part
-- of input_tokens, as the OpenAI-compatible APIs count it, and costs about a
-- tenth of a fresh token. A constant default adds the column without
-- rewriting the table, and the check is NOT VALID, so the lock is held for
-- moments: the next migration fills the column in batches, then validates.
ALTER TABLE runs
    ADD COLUMN cached_input_tokens bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT runs_cached_within_input
        CHECK (cached_input_tokens >= 0 AND cached_input_tokens <= input_tokens) NOT VALID;

-- What a run's budget counts: a cached token as a tenth of one, every other
-- token whole. The one place the weight is written: AddRunUsage returns it,
-- and the usage dashboard and the spend alert sum it. softLines in
-- internal/runtime estimates a next call with the same weight.
CREATE FUNCTION run_spent(input bigint, cached bigint, output bigint) RETURNS bigint
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN input - cached + cached / 10 + output;

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP FUNCTION run_spent(bigint, bigint, bigint);
ALTER TABLE runs DROP COLUMN cached_input_tokens;
