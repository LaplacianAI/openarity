-- +goose Up
SET LOCAL lock_timeout = '3s';

-- How many improver passes a team may run in a day: what a burst of
-- failures can cost before a person has looked.
ALTER TABLE team_settings
    ADD COLUMN improvement_daily_passes smallint NOT NULL DEFAULT 20,
    ADD CONSTRAINT team_settings_daily_passes_range CHECK (improvement_daily_passes BETWEEN 1 AND 200),
    -- What one try at a pass may spend, the screen's tokens included. The
    -- minutes stay under the half hour after which a silent pass is taken
    -- as lost, so a live one never is.
    ADD COLUMN improvement_pass_tokens integer NOT NULL DEFAULT 400000,
    ADD COLUMN improvement_pass_minutes smallint NOT NULL DEFAULT 20,
    ADD CONSTRAINT team_settings_pass_tokens_range CHECK (improvement_pass_tokens BETWEEN 20000 AND 2000000),
    ADD CONSTRAINT team_settings_pass_minutes_range CHECK (improvement_pass_minutes BETWEEN 1 AND 25);

-- One run of the improver for one agent: why it ran, what it was given,
-- what it said, and what it cost. Its proposals point back at it.
CREATE TABLE improver_passes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    agent_id      uuid NOT NULL,
    trigger       text NOT NULL,
    -- The pattern keys it was given, part:target:cause.
    signatures    text[] NOT NULL DEFAULT '{}',
    -- The prune candidates it was given: skill:<name>, tool:<server>/<tool>.
    pruned        text[] NOT NULL DEFAULT '{}',
    -- The runs it might read in full.
    run_ids       uuid[] NOT NULL DEFAULT '{}',
    -- Every run whose text reached it, read in full or as a summary: a
    -- report is shown only to someone who may read them all.
    drew_on       uuid[] NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'running',
    report        text NOT NULL DEFAULT '',
    proposed      int NOT NULL DEFAULT 0,
    screened_out  int NOT NULL DEFAULT 0,
    model         text NOT NULL DEFAULT '',
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    error         text,
    -- The workflow that runs it: a step tried again finds its pass rather
    -- than beginning a second.
    workflow_id   text NOT NULL,
    started_at    timestamptz NOT NULL DEFAULT now(),
    -- When an attempt last began: a running pass not seen for a while is
    -- taken as lost.
    alive_at      timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,

    CONSTRAINT improver_passes_trigger_known CHECK (trigger IN ('pattern', 'daily')),
    CONSTRAINT improver_passes_status_known CHECK (status IN ('running', 'done', 'failed')),
    CONSTRAINT improver_passes_finished CHECK ((status = 'running') = (finished_at IS NULL)),
    CONSTRAINT improver_passes_report_size CHECK (length(report) <= 4000),
    CONSTRAINT improver_passes_error_size CHECK (error IS NULL OR length(error) <= 2000),
    CONSTRAINT improver_passes_counts CHECK (proposed >= 0 AND screened_out >= 0
        AND input_tokens >= 0 AND output_tokens >= 0),
    CONSTRAINT improver_passes_workflow UNIQUE (workflow_id),
    CONSTRAINT improver_passes_agent
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id) ON DELETE CASCADE
);
CREATE INDEX improver_passes_agent_idx ON improver_passes (agent_id, started_at DESC);
CREATE INDEX improver_passes_team_idx ON improver_passes (team_id, started_at DESC, id DESC);
-- One pass under way an agent, however many workers sweep.
CREATE UNIQUE INDEX improver_passes_one_running ON improver_passes (agent_id) WHERE status = 'running';

ALTER TABLE improvement_proposals
    ADD COLUMN pass_id uuid REFERENCES improver_passes (id) ON DELETE SET NULL;
CREATE INDEX improvement_proposals_pass_idx ON improvement_proposals (pass_id) WHERE pass_id IS NOT NULL;
CREATE INDEX improvement_proposals_signature_idx ON improvement_proposals (agent_id, signature, created_at DESC)
    WHERE signature IS NOT NULL;

-- What a pattern counts and a pass reads: one agent's digests that went
-- wrong, by key (I2's index leads with the team).
CREATE INDEX run_digests_agent_wrong_idx ON run_digests (agent_id, part, target, cause, created_at)
    WHERE went = 'wrong';

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP INDEX run_digests_agent_wrong_idx;
DROP INDEX improvement_proposals_signature_idx;
DROP INDEX improvement_proposals_pass_idx;
ALTER TABLE improvement_proposals DROP COLUMN pass_id;
DROP TABLE improver_passes;
ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_pass_minutes_range,
    DROP CONSTRAINT team_settings_pass_tokens_range,
    DROP COLUMN improvement_pass_minutes,
    DROP COLUMN improvement_pass_tokens,
    DROP CONSTRAINT team_settings_daily_passes_range,
    DROP COLUMN improvement_daily_passes;
