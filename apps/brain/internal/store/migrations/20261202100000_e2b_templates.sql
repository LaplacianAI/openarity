-- +goose Up
SET LOCAL lock_timeout = '3s';

-- The room template a team's E2B account is given, built from the release's
-- sandbox image. A template lives in the account its key belongs to, so a row
-- is the build for one team, one sandbox key and one template name: a key
-- pasted from another account starts a build of its own.
CREATE TABLE e2b_templates (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,

    sandbox_secret_id uuid NOT NULL,
    name              text NOT NULL,

    -- E2B's ids for the template and its build, kept once the build is asked
    -- for, so its status can be read back.
    template_id text,
    build_id    text,

    status text NOT NULL,
    error  text,

    started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT e2b_templates_one_per_key UNIQUE (team_id, sandbox_secret_id, name),
    CONSTRAINT e2b_templates_status_known CHECK (status IN ('building', 'ready', 'failed')),
    CONSTRAINT e2b_templates_name_present CHECK (name <> ''),
    -- A build being read back has both ids; a ready one was read back.
    CONSTRAINT e2b_templates_ids_when_asked CHECK (
        status = 'failed' OR (template_id IS NOT NULL AND build_id IS NOT NULL)),
    CONSTRAINT e2b_templates_error_when_failed CHECK (
        (status = 'failed') = (error IS NOT NULL))
);

-- The sweep reads the builds still running.
CREATE INDEX e2b_templates_building_idx ON e2b_templates (updated_at)
    WHERE status = 'building';

-- +goose Down
DROP TABLE e2b_templates;
