-- +goose Up
SET LOCAL lock_timeout = '3s';

-- How an applied change went: the agents' judged runs after it against the
-- ones before, measured against how much those runs usually vary. One row
-- per applied proposal, written with the approve.
CREATE TABLE improvement_watches (
    proposal_id   uuid PRIMARY KEY REFERENCES improvement_proposals (id) ON DELETE CASCADE,
    team_id       uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    -- Who the change reaches, fixed when it was applied.
    agent_ids     uuid[] NOT NULL,
    -- When it was applied: runs created after it ran on the change.
    started_at    timestamptz NOT NULL,
    status        text NOT NULL DEFAULT 'watching',
    flag          text,
    ended_why     text,
    -- Time watched while the team had improvement on, toward the 30 days,
    -- counted from one sweep to the next; null until the first sweep.
    on_seconds    integer NOT NULL DEFAULT 0,
    last_swept_at timestamptz,
    baseline_runs smallint NOT NULL DEFAULT 0,
    baseline_bad  smallint NOT NULL DEFAULT 0,
    -- The bad runs out of ten that chance gives under 5% of the time at the
    -- baseline's rate; null with too little history to judge against.
    bar           smallint,
    before_runs   smallint NOT NULL DEFAULT 0,
    before_bad    smallint NOT NULL DEFAULT 0,
    before_up     smallint NOT NULL DEFAULT 0,
    before_down   smallint NOT NULL DEFAULT 0,
    after_runs    smallint NOT NULL DEFAULT 0,
    after_bad     smallint NOT NULL DEFAULT 0,
    after_up      smallint NOT NULL DEFAULT 0,
    after_down    smallint NOT NULL DEFAULT 0,
    -- Medians per run, and the baseline's 90th percentile of tokens.
    before_tokens bigint,
    after_tokens  bigint,
    baseline_p90  bigint,
    before_steps  integer,
    after_steps   integer,
    -- Runs carrying the targeted signature; null for a prune.
    sig_before    smallint,
    sig_after     smallint,
    -- Other changes applied to the same agents in the windows.
    overlapping   uuid[] NOT NULL DEFAULT '{}',
    finished_at   timestamptz,
    cleared_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    cleared_at    timestamptz,

    CONSTRAINT improvement_watches_status_known
        CHECK (status IN ('watching', 'clear', 'flagged', 'cleared', 'ended')),
    CONSTRAINT improvement_watches_flag_known CHECK (flag IS NULL OR flag IN ('worse', 'costlier', 'both')),
    -- A flag is kept when the change is reverted after it, so the record
    -- still says it was flagged.
    CONSTRAINT improvement_watches_flagged CHECK (status NOT IN ('flagged', 'cleared') OR flag IS NOT NULL),
    CONSTRAINT improvement_watches_flag_settled CHECK (status <> 'watching' OR flag IS NULL),
    CONSTRAINT improvement_watches_ended CHECK ((status = 'ended') = (ended_why IS NOT NULL)),
    CONSTRAINT improvement_watches_ended_known
        CHECK (ended_why IS NULL OR ended_why IN ('reverted', 'too_few_runs')),
    CONSTRAINT improvement_watches_finished CHECK ((status = 'watching') = (finished_at IS NULL)),
    CONSTRAINT improvement_watches_cleared CHECK (status <> 'cleared' OR cleared_at IS NOT NULL),
    CONSTRAINT improvement_watches_cleared_flagged CHECK (cleared_at IS NULL OR flag IS NOT NULL),
    CONSTRAINT improvement_watches_agents CHECK (cardinality(agent_ids) > 0)
);
CREATE INDEX improvement_watches_open_idx ON improvement_watches (last_swept_at NULLS FIRST, started_at) WHERE status = 'watching';
CREATE INDEX improvement_watches_flagged_idx ON improvement_watches (team_id) WHERE status = 'flagged';
CREATE INDEX improvement_watches_agents_idx ON improvement_watches USING gin (agent_ids);

-- Changes applied in the last 30 days are watched too, from when they were
-- applied, counted as swept now with the time since. A skill edit reaches
-- the agents that hold the skill now.
INSERT INTO improvement_watches (proposal_id, team_id, agent_ids, started_at, on_seconds, last_swept_at)
SELECT p.id, p.team_id,
       CASE WHEN p.kind = 'skill_edit'
            THEN coalesce((SELECT array_agg(s.agent_id) FROM agent_skills s WHERE s.skill_id = p.skill_id),
                          ARRAY[p.agent_id])
            ELSE ARRAY[p.agent_id] END,
       p.decided_at,
       least(extract(epoch FROM now() - p.decided_at), 2147483647)::integer,
       now()
FROM improvement_proposals p
WHERE p.status = 'applied' AND p.decided_at > now() - interval '30 days';

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE improvement_watches;
