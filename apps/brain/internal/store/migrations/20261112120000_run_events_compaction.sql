-- +goose Up
SET lock_timeout = '3s';

-- A run compacting its context, and being asked to finish before its budget
-- or its time runs out. A migration of its own, as text's was, so a database
-- that ran the earlier ones gets these too.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up')) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

DELETE FROM run_events WHERE kind IN ('compacted', 'wrap_up');
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text')) NOT VALID;
