-- +goose Up
SET lock_timeout = '3s';

-- A builder run's reply, recorded as it streams: pieces of the model's text,
-- in order. Its own migration, so a database that ran add_builder before
-- text existed gets it too.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text')) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

DELETE FROM run_events WHERE kind = 'text';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed')) NOT VALID;
