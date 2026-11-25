-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A run hands its person a file of the conversation (send_file): a reply of
-- kind file, in the session's order like every reply, so a file sent while
-- the run works goes out before its answer. It names the attachment and the
-- call that sent it; the call keeps a retried call to one reply. text is the
-- file's name, and detail its caption, if any.
ALTER TABLE replies
    ADD COLUMN attachment_id uuid,
    ADD COLUMN call_id text;

-- The pair, as an attachment names its run: a reply cannot send a file of
-- another session. A null attachment_id is not checked (MATCH SIMPLE).
-- Deleting the file deletes a reply still waiting to send it, as there is
-- nothing left to send.
ALTER TABLE replies
    ADD CONSTRAINT replies_file_in_session
        FOREIGN KEY (attachment_id, session_id) REFERENCES attachments (id, session_id)
        ON DELETE CASCADE NOT VALID;

-- NOT VALID, as every replacement of a kind check has been: the rows already
-- there were written under the last.
ALTER TABLE replies
    DROP CONSTRAINT replies_kind_known,
    ADD CONSTRAINT replies_kind_known
        CHECK (kind IN ('answer', 'apology', 'event', 'approval', 'notice', 'file')) NOT VALID,
    ADD CONSTRAINT replies_file_names_its_file CHECK (
        (kind = 'file') = (attachment_id IS NOT NULL)
        AND (kind = 'file') = (call_id IS NOT NULL)
        AND (call_id IS NULL OR call_id <> '')
    ) NOT VALID,
    ADD CONSTRAINT replies_call_id_bounded CHECK (char_length(call_id) <= 255) NOT VALID;

-- A run's file_sent event: what the dashboard shows under the run. NOT VALID,
-- as every replacement of this check has been.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting', 'skipped', 'file_sent')) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '3s';

DELETE FROM run_events WHERE kind = 'file_sent';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting', 'skipped')) NOT VALID;

-- A file a run sent was sent or is moot once the kind is gone; the answers,
-- events, approvals and notices stay.
DELETE FROM replies WHERE kind = 'file';

ALTER TABLE replies
    DROP CONSTRAINT replies_call_id_bounded,
    DROP CONSTRAINT replies_file_names_its_file,
    DROP CONSTRAINT replies_kind_known,
    ADD CONSTRAINT replies_kind_known
        CHECK (kind IN ('answer', 'apology', 'event', 'approval', 'notice')) NOT VALID,
    DROP CONSTRAINT replies_file_in_session,
    DROP COLUMN call_id,
    DROP COLUMN attachment_id;
