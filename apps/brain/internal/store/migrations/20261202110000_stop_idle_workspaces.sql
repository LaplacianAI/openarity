-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A workspace room is stopped, not deleted, when its session has nothing
-- running: it gives its memory back and keeps its files. stopped_at is when;
-- null is a room that runs.
ALTER TABLE sandbox_rooms ADD COLUMN stopped_at timestamptz;

-- The reaper's sweep for stopped rooms past their keep hours reads only
-- these; a running room has none.
CREATE INDEX sandbox_rooms_stopped_idx ON sandbox_rooms (stopped_at) WHERE stopped_at IS NOT NULL;

-- How long a team's stopped workspace keeps its files before it is deleted,
-- and how long a running one may go unused before it is stopped. Null idle
-- minutes is the deployment's OPENARITY_SANDBOX_IDLE.
ALTER TABLE team_settings
    ADD COLUMN sandbox_keep_hours   integer NOT NULL DEFAULT 24,
    ADD COLUMN sandbox_idle_minutes integer,
    ADD CONSTRAINT team_settings_sandbox_keep_hours_range CHECK (sandbox_keep_hours BETWEEN 1 AND 720),
    ADD CONSTRAINT team_settings_sandbox_idle_minutes_range CHECK (
        sandbox_idle_minutes IS NULL OR sandbox_idle_minutes BETWEEN 1 AND 1440);

-- The size of the team's workspace rooms. Null is the provider's own: the
-- operator's for sandboxd, 2 vCPUs and 2 GiB for E2B, Daytona's default.
-- The provider refuses a size past what it allows; these bounds are only
-- the ones no provider passes.
ALTER TABLE team_settings
    ADD COLUMN sandbox_workspace_memory_mb integer,
    ADD COLUMN sandbox_workspace_cpus      integer,
    ADD COLUMN sandbox_workspace_disk_mb   integer,
    ADD CONSTRAINT team_settings_sandbox_workspace_memory_range CHECK (
        sandbox_workspace_memory_mb IS NULL OR sandbox_workspace_memory_mb BETWEEN 256 AND 65536),
    ADD CONSTRAINT team_settings_sandbox_workspace_cpus_range CHECK (
        sandbox_workspace_cpus IS NULL OR sandbox_workspace_cpus BETWEEN 1 AND 16),
    ADD CONSTRAINT team_settings_sandbox_workspace_disk_range CHECK (
        sandbox_workspace_disk_mb IS NULL OR sandbox_workspace_disk_mb BETWEEN 256 AND 262144),
    -- A size is set whole or not at all, so a provider is never sent half.
    ADD CONSTRAINT team_settings_sandbox_workspace_size_whole CHECK (
        (sandbox_workspace_memory_mb IS NULL) = (sandbox_workspace_cpus IS NULL)
        AND (sandbox_workspace_cpus IS NULL) = (sandbox_workspace_disk_mb IS NULL));

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_sandbox_workspace_size_whole,
    DROP CONSTRAINT team_settings_sandbox_workspace_disk_range,
    DROP CONSTRAINT team_settings_sandbox_workspace_cpus_range,
    DROP CONSTRAINT team_settings_sandbox_workspace_memory_range,
    DROP COLUMN sandbox_workspace_disk_mb,
    DROP COLUMN sandbox_workspace_cpus,
    DROP COLUMN sandbox_workspace_memory_mb;

ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_sandbox_idle_minutes_range,
    DROP CONSTRAINT team_settings_sandbox_keep_hours_range,
    DROP COLUMN sandbox_idle_minutes,
    DROP COLUMN sandbox_keep_hours;

DROP INDEX sandbox_rooms_stopped_idx;
ALTER TABLE sandbox_rooms DROP COLUMN stopped_at;
