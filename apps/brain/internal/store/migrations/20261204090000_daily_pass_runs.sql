-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A daily pass waits for this many judged runs since the agent's last pass,
-- so one is not spent on an agent with too little to read.
ALTER TABLE team_settings
    ADD COLUMN improvement_daily_runs smallint NOT NULL DEFAULT 5,
    ADD CONSTRAINT team_settings_daily_runs_range CHECK (improvement_daily_runs BETWEEN 1 AND 100);

-- How many judged runs a daily pass had since the agent's last pass; null
-- for a pass a pattern started, or one from before this was counted.
ALTER TABLE improver_passes ADD COLUMN runs_seen integer;

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE improver_passes DROP COLUMN runs_seen;
ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_daily_runs_range,
    DROP COLUMN improvement_daily_runs;
