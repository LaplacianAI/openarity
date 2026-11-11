-- +goose Up
SET lock_timeout = '3s';

-- A builder run is the team's built-in builder answering at the root: it reads
-- the team and proposes blueprints, and writes nothing. Never also the hub.
ALTER TABLE runs ADD COLUMN builder boolean NOT NULL DEFAULT false;
ALTER TABLE runs ADD CONSTRAINT runs_builder_is_a_root
    CHECK (NOT builder OR parent_run_id IS NULL) NOT VALID;
ALTER TABLE runs ADD CONSTRAINT runs_builder_is_not_the_hub
    CHECK (NOT (builder AND hub)) NOT VALID;

-- What a builder run proposed: a blueprint, and the plan it had when it was
-- made. A person applies it, as themselves, after it is planned again. A newer
-- proposal in the same session supersedes the open one, so a session has at
-- most one open proposal.
CREATE TABLE proposals (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id     uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    session_id  uuid NOT NULL,
    run_id      uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    summary     text NOT NULL,
    blueprint   jsonb NOT NULL,
    plan        jsonb NOT NULL,
    state       text NOT NULL DEFAULT 'open',
    applied_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    applied_at  timestamptz,
    CONSTRAINT proposals_state_known CHECK (state IN ('open', 'applied', 'superseded')),
    CONSTRAINT proposals_applied_when CHECK ((state = 'applied') = (applied_at IS NOT NULL))
);
CREATE INDEX proposals_by_team ON proposals (team_id, created_at DESC, id DESC);
CREATE INDEX proposals_by_run ON proposals (run_id);
CREATE UNIQUE INDEX proposals_one_open_per_session ON proposals (session_id) WHERE state = 'open';

ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text')) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

DELETE FROM run_events WHERE kind IN ('proposed', 'text');
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan')) NOT VALID;

DROP TABLE proposals;
ALTER TABLE runs DROP COLUMN builder;
