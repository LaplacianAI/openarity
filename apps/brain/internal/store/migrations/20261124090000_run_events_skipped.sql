-- +goose Up
SET LOCAL lock_timeout = '3s';
-- A pattern's own step left out, and why: a review that came back unreadable,
-- with the answer it was reviewing kept. NOT VALID, as every replacement of
-- this check has been: the rows already there were written under the last.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting', 'skipped')) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '3s';
DELETE FROM run_events WHERE kind = 'skipped';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting')) NOT VALID;
