-- +goose Up
SET LOCAL lock_timeout = '3s';

-- The tokens a run's next request may read before it compacts: a tenth of
-- the run's budget, held between these two. They were the brain's constants,
-- 50,000 and 60,000, which stay the defaults. Below 30,000 a run would compact
-- almost every step, since a compaction keeps about 15,000 tokens of the latest
-- turns besides the prompt and the tools; 1,000,000 is the largest context a
-- model takes. A constant default adds the columns without rewriting the table.
ALTER TABLE team_settings
    ADD COLUMN compaction_tokens_min integer NOT NULL DEFAULT 50000,
    ADD COLUMN compaction_tokens_max integer NOT NULL DEFAULT 60000,
    ADD CONSTRAINT team_settings_compaction_tokens_min_range
        CHECK (compaction_tokens_min BETWEEN 30000 AND 1000000),
    ADD CONSTRAINT team_settings_compaction_tokens_max_range
        CHECK (compaction_tokens_max BETWEEN 30000 AND 1000000),
    ADD CONSTRAINT team_settings_compaction_tokens_order
        CHECK (compaction_tokens_min <= compaction_tokens_max);

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE team_settings
    DROP COLUMN compaction_tokens_max,
    DROP COLUMN compaction_tokens_min;
