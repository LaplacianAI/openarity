-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A replay: a proposal tried on a few of its agent's past runs before anyone
-- decides on it. Off until a team turns it on, and every number it uses the
-- team's own.
ALTER TABLE team_settings
    ADD COLUMN improvement_replay         boolean  NOT NULL DEFAULT false,
    ADD COLUMN improvement_replay_runs    smallint NOT NULL DEFAULT 4,
    ADD COLUMN improvement_replay_tokens  integer  NOT NULL DEFAULT 200000,
    ADD COLUMN improvement_replay_minutes smallint NOT NULL DEFAULT 10,
    ADD COLUMN improvement_replay_daily   smallint NOT NULL DEFAULT 40,
    ADD CONSTRAINT team_settings_replay_runs_range    CHECK (improvement_replay_runs BETWEEN 1 AND 10),
    ADD CONSTRAINT team_settings_replay_tokens_range  CHECK (improvement_replay_tokens BETWEEN 20000 AND 1000000),
    ADD CONSTRAINT team_settings_replay_minutes_range CHECK (improvement_replay_minutes BETWEEN 1 AND 30),
    ADD CONSTRAINT team_settings_replay_daily_range   CHECK (improvement_replay_daily BETWEEN 1 AND 500);

-- One past run a proposal was tried on, and what came of it. A replay writes
-- no run, event, message or reply: this row is all it leaves.
CREATE TABLE improvement_replays (
    proposal_id   uuid NOT NULL REFERENCES improvement_proposals (id) ON DELETE CASCADE,
    team_id       uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    source_run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    -- Its place in the proposal's set: cited runs first, then good ones.
    position      smallint NOT NULL,
    -- Why it was chosen: a run the proposal cites, or a recent good one.
    chosen        text NOT NULL,
    status        text NOT NULL DEFAULT 'queued',
    skip_why      text,
    error         text,
    -- The original run's outcome, and the replay's, as I2's Effective has it.
    original      text NOT NULL,
    replayed      text,
    -- The judge's verdict on the replay, and why.
    verdict       text,
    why           text,
    -- Whether a call was not made because the original never made it and it
    -- was not a read, and how each call was served.
    diverged      boolean  NOT NULL DEFAULT false,
    recorded      smallint NOT NULL DEFAULT 0,
    live          smallint NOT NULL DEFAULT 0,
    not_made      smallint NOT NULL DEFAULT 0,
    input_tokens  bigint   NOT NULL DEFAULT 0,
    cached_input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint   NOT NULL DEFAULT 0,
    judge_tokens  bigint   NOT NULL DEFAULT 0,
    steps         integer  NOT NULL DEFAULT 0,
    work_ms       bigint   NOT NULL DEFAULT 0,
    -- What the past run spent itself, kept when it was chosen: it is
    -- finished, so this does not change, and a list need not add it up.
    original_spent   bigint  NOT NULL DEFAULT 0,
    original_steps   integer NOT NULL DEFAULT 0,
    original_work_ms bigint  NOT NULL DEFAULT 0,
    answer        text,
    trace         jsonb    NOT NULL DEFAULT '[]',
    created_at    timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    finished_at   timestamptz,
    PRIMARY KEY (proposal_id, source_run_id),
    CONSTRAINT improvement_replays_chosen_known   CHECK (chosen IN ('cited', 'good')),
    CONSTRAINT improvement_replays_status_known   CHECK (status IN ('queued', 'trying', 'done', 'failed', 'skipped')),
    CONSTRAINT improvement_replays_original_known CHECK (original IN ('good', 'bad')),
    CONSTRAINT improvement_replays_replayed_known CHECK (replayed IS NULL OR replayed IN ('good', 'bad', 'cant_tell')),
    CONSTRAINT improvement_replays_done           CHECK ((status = 'done') = (replayed IS NOT NULL)),
    CONSTRAINT improvement_replays_skipped        CHECK ((status = 'skipped') = (skip_why IS NOT NULL)),
    CONSTRAINT improvement_replays_failed         CHECK ((status = 'failed') = (error IS NOT NULL)),
    CONSTRAINT improvement_replays_finished       CHECK ((status IN ('done', 'failed', 'skipped')) = (finished_at IS NOT NULL)),
    CONSTRAINT improvement_replays_answer_size    CHECK (answer IS NULL OR octet_length(answer) <= 16384),
    CONSTRAINT improvement_replays_trace_size     CHECK (octet_length(trace::text) <= 262144)
);

-- When a proposal's past runs were chosen, with trying on: a proposal with
-- this set and no tries is one none of whose runs could be tried, told apart
-- from one proposed while trying was off.
ALTER TABLE improvement_proposals ADD COLUMN replay_chosen_at timestamptz;

-- A team's tries today, for its daily cap.
CREATE INDEX improvement_replays_team_started ON improvement_replays (team_id, started_at)
    WHERE started_at IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE improvement_replays;

ALTER TABLE improvement_proposals DROP COLUMN replay_chosen_at;

ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_replay_daily_range,
    DROP CONSTRAINT team_settings_replay_minutes_range,
    DROP CONSTRAINT team_settings_replay_tokens_range,
    DROP CONSTRAINT team_settings_replay_runs_range,
    DROP COLUMN improvement_replay_daily,
    DROP COLUMN improvement_replay_minutes,
    DROP COLUMN improvement_replay_tokens,
    DROP COLUMN improvement_replay_runs,
    DROP COLUMN improvement_replay;
