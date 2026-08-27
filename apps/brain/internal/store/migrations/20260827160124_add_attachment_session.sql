-- +goose Up
SET lock_timeout = '3s';

-- "Every attachment in this session" is what an agent asks to build context: a
-- message can name a file that arrived twenty messages earlier. No index
-- expresses that through attachments -> messages, so the join reads every
-- message in the session and every attachment in the database to return the
-- few hundred that match. Measured on 200 sessions, 209k messages and 21k
-- attachments, asking for one session's:
--
--   through messages   13.6 ms warm, 488 ms cold, 591 buffers
--   this column         0.36 ms warm, 0.65 ms cold,  25 buffers
--
-- Neither side of the join narrows, and a long session — the case where the
-- question actually gets asked — is what makes the message side grow.
--
-- This is a second copy of a fact the message already carries. Redundancy
-- against performance is the usual trade and half of it is absent here: the
-- composite foreign key below means the copy cannot drift, so what is left is
-- sixteen bytes and an index. The schema already makes the same trade for
-- sessions.team_id against channels.team_id, by the same mechanism.
ALTER TABLE attachments ADD COLUMN session_id uuid;

-- Nothing writes attachments yet, so this is empty in every deployment that
-- exists. It is here so the migration is correct rather than only correct
-- today: a table with rows would need this before the NOT NULL below.
UPDATE attachments a
SET session_id = m.session_id
FROM messages m
WHERE m.id = a.message_id;

ALTER TABLE attachments ALTER COLUMN session_id SET NOT NULL;

-- messages already has a primary key on id. This second unique key is what
-- lets attachments reference the pair, which is the whole mechanism.
ALTER TABLE messages ADD CONSTRAINT messages_id_session_key UNIQUE (id, session_id);

-- The pair, not the id alone. An attachment naming a session its message is
-- not in cannot be written, so the copy is a fact the database keeps rather
-- than one every writer is trusted with.
--
-- This replaces the single-column reference rather than joining it: two
-- foreign keys onto the same table would both fire, and the composite one
-- already implies the simple one. An unknown message is refused by this
-- constraint now.
ALTER TABLE attachments DROP CONSTRAINT attachments_message_id_fkey;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_message_in_session
        FOREIGN KEY (message_id, session_id) REFERENCES messages (id, session_id)
        ON DELETE CASCADE;

-- Listing what a session has accumulated. The reason the column exists.
CREATE INDEX attachments_session_id_idx ON attachments (session_id);

-- +goose Down
DROP INDEX attachments_session_id_idx;

ALTER TABLE attachments DROP CONSTRAINT attachments_message_in_session;

ALTER TABLE attachments
    ADD CONSTRAINT attachments_message_id_fkey
        FOREIGN KEY (message_id) REFERENCES messages (id) ON DELETE CASCADE;

ALTER TABLE messages DROP CONSTRAINT messages_id_session_key;

ALTER TABLE attachments DROP COLUMN session_id;
