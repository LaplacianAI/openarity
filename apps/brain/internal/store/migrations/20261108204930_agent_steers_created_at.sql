-- When a steer was written, so a run's reply can quote the newest message the
-- run read and not one that arrived after its last request. The default is
-- the same for every row added by this statement, so Postgres stores it once
-- and rewrites nothing; the SDK names its columns, so its inserts are unchanged.

-- +goose Up
ALTER TABLE agent_steers ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE agent_steers DROP COLUMN created_at;
