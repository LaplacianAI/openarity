-- +goose Up
SET lock_timeout = '3s';

-- What a streaming run thinks rather than says, recorded as it arrives: the
-- model's own reasoning, and a pattern's plan or critique.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning')) NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

DELETE FROM run_events WHERE kind = 'reasoning';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed')) NOT VALID;
