-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A file a tool returned is kept as its conversation's attachment, as a file
-- a person sent is: one table, so the reads, the bytes route and the
-- tombstone trigger serve both. Its source is a run's call, not a message.
ALTER TABLE attachments
    ALTER COLUMN message_id DROP NOT NULL,
    ADD COLUMN run_id uuid,
    -- the tool's name as the model called it, and the call that returned the
    -- file: both from the run, bounded as a provider's filename is
    ADD COLUMN tool text,
    ADD COLUMN call_id text;

-- The pair, as a message's file names its message: a run's file cannot name
-- a run of another session. A null run_id is not checked (MATCH SIMPLE).
-- Deleting the run deletes its files, and the trigger tombstones their
-- objects.
ALTER TABLE attachments
    ADD CONSTRAINT attachments_run_in_session
        FOREIGN KEY (run_id, session_id) REFERENCES runs (id, session_id)
        ON DELETE CASCADE NOT VALID;

-- Exactly one source: a person's message, or a run's call with its tool.
-- Every row before this has a message and none of the new columns.
ALTER TABLE attachments
    ADD CONSTRAINT attachments_one_source CHECK (
        (message_id IS NOT NULL AND run_id IS NULL AND tool IS NULL AND call_id IS NULL)
     OR (message_id IS NULL AND run_id IS NOT NULL AND tool <> '' AND call_id <> '')
    ) NOT VALID,
    ADD CONSTRAINT attachments_tool_bounded CHECK (char_length(tool) <= 255) NOT VALID,
    ADD CONSTRAINT attachments_call_id_bounded CHECK (char_length(call_id) <= 255) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '3s';

-- A run's files have no message, so they cannot stay once message_id is
-- required again; the trigger tombstones their objects.
DELETE FROM attachments WHERE run_id IS NOT NULL;

ALTER TABLE attachments
    DROP CONSTRAINT attachments_call_id_bounded,
    DROP CONSTRAINT attachments_tool_bounded,
    DROP CONSTRAINT attachments_one_source,
    DROP CONSTRAINT attachments_run_in_session,
    DROP COLUMN call_id,
    DROP COLUMN tool,
    DROP COLUMN run_id,
    ALTER COLUMN message_id SET NOT NULL;
