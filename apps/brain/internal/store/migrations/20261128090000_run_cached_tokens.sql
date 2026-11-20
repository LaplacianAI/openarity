-- +goose Up
SET LOCAL lock_timeout = '3s';

-- How much of a run's input the provider served from its cache. It is part
-- of input_tokens, as the OpenAI-compatible APIs count it, and costs about a
-- tenth of a fresh token: a run's budget counts it so, and the usage
-- dashboard shows it apart. Filled for the runs before it from their usage
-- events, which have always carried it.
ALTER TABLE runs
    ADD COLUMN cached_input_tokens bigint NOT NULL DEFAULT 0;

UPDATE runs r SET cached_input_tokens = least(u.cached, r.input_tokens)
FROM (
    SELECT run_id, sum((payload->>'cached_input_tokens')::bigint) AS cached
    FROM run_events
    WHERE kind = 'usage' AND payload ? 'cached_input_tokens'
    GROUP BY run_id
) u
WHERE u.run_id = r.id AND u.cached > 0;

ALTER TABLE runs ADD CONSTRAINT runs_cached_within_input
    CHECK (cached_input_tokens >= 0 AND cached_input_tokens <= input_tokens);

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE runs DROP COLUMN cached_input_tokens;
