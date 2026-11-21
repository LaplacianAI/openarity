-- +goose Up
SET LOCAL lock_timeout = '3s';

-- What judges and digests a team's runs, and how much of what went well is
-- digested. improvement_since is when the switch last went on: runs before
-- it are never judged.
ALTER TABLE team_settings
    ADD COLUMN judge_model_name         text,
    ADD COLUMN judge_model_max_tokens   integer,
    ADD COLUMN judge_model_temperature  double precision,
    ADD COLUMN improvement_sample_share smallint NOT NULL DEFAULT 10,
    ADD COLUMN improvement_since        timestamptz,
    ADD CONSTRAINT team_settings_judge_model_whole
        CHECK ((judge_model_name IS NULL) = (judge_model_max_tokens IS NULL)),
    ADD CONSTRAINT team_settings_judge_model_shaped
        CHECK (judge_model_name IS NULL OR (btrim(judge_model_name) <> '' AND judge_model_max_tokens > 0)),
    ADD CONSTRAINT team_settings_judge_temperature_range
        CHECK (judge_model_temperature IS NULL OR judge_model_temperature BETWEEN 0 AND 2),
    ADD CONSTRAINT team_settings_sample_share_range
        CHECK (improvement_sample_share BETWEEN 0 AND 100);

-- A team that already had the switch on starts from now: its past runs are
-- not judged, as a team turning it on from here would find.
UPDATE team_settings SET improvement_since = now() WHERE improvement;

-- Was the task done? One row per judged top-level run.
CREATE TABLE run_outcomes (
    run_id        uuid PRIMARY KEY,
    team_id       uuid NOT NULL,
    verdict       text NOT NULL,
    -- What the people's next messages say about it.
    implicit      text NOT NULL DEFAULT 'none',
    asked         jsonb NOT NULL DEFAULT '[]',
    missed        text NOT NULL DEFAULT '',
    why           text NOT NULL DEFAULT '',
    -- Why it is cant_tell, when the call failed or answered nonsense.
    error         text,
    model         text NOT NULL,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    -- In the share of good runs digested, by the run id's hash.
    sampled       boolean NOT NULL,
    -- pending: to be digested; skipped: not needed; done: digested.
    digest        text NOT NULL,
    -- Raised each time a skipped digest is reopened, so the digest job's
    -- workflow id is new and it runs again.
    digest_round  integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT run_outcomes_run FOREIGN KEY (run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT run_outcomes_verdict CHECK (verdict IN ('done', 'partly', 'not_done', 'wrong', 'cant_tell')),
    CONSTRAINT run_outcomes_implicit CHECK (implicit IN ('none', 'correction', 'repeat', 'retry', 'thanks')),
    CONSTRAINT run_outcomes_asked_array CHECK (jsonb_typeof(asked) = 'array'),
    CONSTRAINT run_outcomes_text_bounded CHECK (length(missed) <= 2000 AND length(why) <= 2000),
    CONSTRAINT run_outcomes_digest CHECK (digest IN ('pending', 'skipped', 'done'))
);
CREATE INDEX run_outcomes_pending_idx ON run_outcomes (team_id) WHERE digest = 'pending';

-- A person's word on an answer: one per person per run.
CREATE TABLE run_feedback (
    run_id     uuid NOT NULL,
    team_id    uuid NOT NULL,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    value      text NOT NULL,
    note       text NOT NULL DEFAULT '',
    via        text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, user_id),
    CONSTRAINT run_feedback_run FOREIGN KEY (run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT run_feedback_value CHECK (value IN ('up', 'down')),
    CONSTRAINT run_feedback_note_bounded CHECK (length(note) <= 1000),
    CONSTRAINT run_feedback_via CHECK (via IN ('dashboard', 'cli'))
);

-- What one custom agent did in a digested run, and the signature I3 counts
-- patterns by: part, target and cause.
CREATE TABLE run_digests (
    run_id        uuid NOT NULL,
    agent_id      uuid NOT NULL,
    team_id       uuid NOT NULL,
    went          text NOT NULL,
    summary       text NOT NULL,
    part          text NOT NULL,
    target        text NOT NULL DEFAULT '',
    cause         text NOT NULL,
    behaviour     text NOT NULL,
    -- Someone who is not a member spoke in the session it came from.
    outsider      boolean NOT NULL DEFAULT false,
    model         text NOT NULL,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, agent_id),
    CONSTRAINT run_digests_run FOREIGN KEY (run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT run_digests_agent FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id) ON DELETE CASCADE,
    CONSTRAINT run_digests_went CHECK (went IN ('wrong', 'well')),
    CONSTRAINT run_digests_summary_bounded CHECK (summary <> '' AND length(summary) <= 1200),
    CONSTRAINT run_digests_part CHECK (part IN ('instructions', 'description', 'skill', 'tool', 'server',
        'limits', 'delegation', 'pattern', 'none')),
    CONSTRAINT run_digests_target_bounded CHECK (length(target) <= 200),
    CONSTRAINT run_digests_cause CHECK (cause IN ('missing_knowledge', 'skipped_step', 'wrong_tool', 'misused_tool',
        'misread_request', 'gave_up', 'ran_out', 'wrong_delegate', 'external', 'other')),
    CONSTRAINT run_digests_behaviour_bounded CHECK (behaviour <> '' AND length(behaviour) <= 300)
);
CREATE INDEX run_digests_pattern_idx ON run_digests (team_id, agent_id, part, target, cause, created_at);

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE run_digests;
DROP TABLE run_feedback;
DROP TABLE run_outcomes;
ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_sample_share_range,
    DROP CONSTRAINT team_settings_judge_temperature_range,
    DROP CONSTRAINT team_settings_judge_model_shaped,
    DROP CONSTRAINT team_settings_judge_model_whole,
    DROP COLUMN improvement_since,
    DROP COLUMN improvement_sample_share,
    DROP COLUMN judge_model_temperature,
    DROP COLUMN judge_model_max_tokens,
    DROP COLUMN judge_model_name;
