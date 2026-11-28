-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A run telling its person something while it works (reply): a said event,
-- which the dashboard shows as the run's message and a channel is sent as a
-- step. NOT VALID, as every replacement of this check has been.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting', 'skipped', 'file_sent',
    'said')) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '3s';

-- What a run said was sent or is moot once the kind is gone. Its replies are
-- steps like any other and stay, sent or sending.
DELETE FROM run_events WHERE kind = 'said';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting', 'skipped', 'file_sent')) NOT VALID;
