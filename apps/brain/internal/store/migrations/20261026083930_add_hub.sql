-- +goose Up
SET lock_timeout = '3s';

-- A run is the hub's when it is a POST /runs that names no agent (and, from
-- slice E, a message's root run). A hub run with no agent is the built-in
-- orchestrator. A delegate's run is never the hub's.
ALTER TABLE runs ADD COLUMN hub boolean NOT NULL DEFAULT false;
ALTER TABLE runs ADD CONSTRAINT runs_hub_is_a_root
    CHECK (NOT hub OR parent_run_id IS NULL) NOT VALID;

-- The team's chosen hub. Deleting the agent returns the team to the built-in
-- orchestrator. The same team is the API's to keep: a composite key would
-- null team_id too, and Postgres 13 has no ON DELETE SET NULL (column).
ALTER TABLE team_settings ADD COLUMN hub_agent_id uuid
    REFERENCES agents (id) ON DELETE SET NULL;

ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan')) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

DELETE FROM run_events WHERE kind = 'plan';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated'));

ALTER TABLE team_settings DROP COLUMN hub_agent_id;
ALTER TABLE runs DROP COLUMN hub;
