-- +goose Up
SET LOCAL lock_timeout = '3s';

-- An efficiency pass: the improver looking at an agent whose runs go well
-- but whose costliest spend far more than the rest. Off until a team turns
-- it on, and every number it uses the team's own.
ALTER TABLE team_settings
    ADD COLUMN improvement_efficiency           boolean  NOT NULL DEFAULT false,
    ADD COLUMN improvement_efficiency_runs      smallint NOT NULL DEFAULT 20,
    ADD COLUMN improvement_efficiency_days      smallint NOT NULL DEFAULT 14,
    ADD COLUMN improvement_efficiency_spread    smallint NOT NULL DEFAULT 200,
    ADD COLUMN improvement_efficiency_mark_runs smallint NOT NULL DEFAULT 3,
    ADD COLUMN improvement_efficiency_margin    smallint NOT NULL DEFAULT 1,
    ADD CONSTRAINT team_settings_efficiency_runs_range   CHECK (improvement_efficiency_runs BETWEEN 10 AND 500),
    ADD CONSTRAINT team_settings_efficiency_days_range   CHECK (improvement_efficiency_days BETWEEN 7 AND 60),
    ADD CONSTRAINT team_settings_efficiency_spread_range CHECK (improvement_efficiency_spread BETWEEN 125 AND 1000),
    ADD CONSTRAINT team_settings_efficiency_marks_range  CHECK (improvement_efficiency_mark_runs BETWEEN 2 AND 50
                                                            AND improvement_efficiency_mark_runs <= improvement_efficiency_runs),
    ADD CONSTRAINT team_settings_efficiency_margin_range CHECK (improvement_efficiency_margin BETWEEN 0 AND 3);

ALTER TABLE improver_passes
    DROP CONSTRAINT improver_passes_trigger_known,
    ADD CONSTRAINT improver_passes_trigger_known CHECK (trigger IN ('pattern', 'daily', 'efficiency'));

-- What a proposal is for: a run that went wrong, or a good run made cheaper
-- or quicker, with which of the two it aims to save.
ALTER TABLE improvement_proposals
    ADD COLUMN goal text NOT NULL DEFAULT 'quality',
    ADD COLUMN saves text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT improvement_proposals_goal_known CHECK (goal IN ('quality', 'efficiency')),
    ADD CONSTRAINT improvement_proposals_saves_known CHECK (saves <@ ARRAY['cost', 'time']::text[]),
    ADD CONSTRAINT improvement_proposals_saves CHECK ((goal = 'efficiency') = (cardinality(saves) > 0));

-- An efficiency change whose good runs did not get cheaper or quicker is
-- flagged so; the median work time of the good runs either side is kept
-- beside their tokens.
ALTER TABLE improvement_watches
    DROP CONSTRAINT improvement_watches_flag_known,
    ADD CONSTRAINT improvement_watches_flag_known CHECK (flag IS NULL OR flag IN ('worse', 'costlier', 'both', 'no_saving')),
    ADD COLUMN before_work_ms bigint,
    ADD COLUMN after_work_ms  bigint;

-- How long a run's tree spent working, in milliseconds: the gaps between its
-- events in the order they were written, leaving out a gap that ends in a
-- run resuming. That is mostly time spent waiting for a person's approval;
-- a run taken over by another worker after its first was lost also resumes,
-- and that wait is not the agent's work either. Events of one tree written
-- in the same instant fall back to run and sequence order; their gap is
-- nothing either way. The watch and the efficiency sweep both count it here,
-- so they agree. It reads one tree's events by the runs' root index and the
-- events' primary key, so it costs what that tree's events are.
-- +goose StatementBegin
CREATE FUNCTION run_work_ms(root uuid) RETURNS bigint
LANGUAGE sql STABLE AS $$
    SELECT coalesce(sum(g.gap) FILTER (WHERE g.kind <> 'resumed'), 0)::bigint
    FROM (
        SELECT e.kind,
               extract(epoch FROM e.created_at - lag(e.created_at) OVER (ORDER BY e.created_at, e.run_id, e.seq)) * 1000 AS gap
        FROM run_events e JOIN runs t ON t.id = e.run_id
        WHERE t.root_run_id = root
    ) g
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP FUNCTION run_work_ms(uuid);

-- This Down loses what the earlier checks cannot hold, and says so: a
-- no_saving flag becomes costlier (the nearest, as it was flagged on what
-- the change spent), and an efficiency pass becomes daily. Migrating up again
-- does not bring either back. Proposals lose their goal and saving.
UPDATE improvement_watches SET flag = 'costlier' WHERE flag = 'no_saving';
ALTER TABLE improvement_watches
    DROP COLUMN after_work_ms,
    DROP COLUMN before_work_ms,
    DROP CONSTRAINT improvement_watches_flag_known,
    ADD CONSTRAINT improvement_watches_flag_known CHECK (flag IS NULL OR flag IN ('worse', 'costlier', 'both'));

ALTER TABLE improvement_proposals
    DROP CONSTRAINT improvement_proposals_saves,
    DROP CONSTRAINT improvement_proposals_saves_known,
    DROP CONSTRAINT improvement_proposals_goal_known,
    DROP COLUMN saves,
    DROP COLUMN goal;

UPDATE improver_passes SET trigger = 'daily' WHERE trigger = 'efficiency';
ALTER TABLE improver_passes
    DROP CONSTRAINT improver_passes_trigger_known,
    ADD CONSTRAINT improver_passes_trigger_known CHECK (trigger IN ('pattern', 'daily'));

ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_efficiency_margin_range,
    DROP CONSTRAINT team_settings_efficiency_marks_range,
    DROP CONSTRAINT team_settings_efficiency_spread_range,
    DROP CONSTRAINT team_settings_efficiency_days_range,
    DROP CONSTRAINT team_settings_efficiency_runs_range,
    DROP COLUMN improvement_efficiency_margin,
    DROP COLUMN improvement_efficiency_mark_runs,
    DROP COLUMN improvement_efficiency_spread,
    DROP COLUMN improvement_efficiency_days,
    DROP COLUMN improvement_efficiency_runs,
    DROP COLUMN improvement_efficiency;
