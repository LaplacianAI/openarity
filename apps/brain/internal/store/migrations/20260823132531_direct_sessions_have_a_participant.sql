-- +goose Up
SET lock_timeout = '3s';

-- Who a conversation is with, and the whole of what makes a direct session
-- private. Every session was readable by every member of the team, which is
-- the right rule for a shared channel and the wrong one for a direct message:
-- the same query that lets a team read its support channel let it read
-- somebody's private message to an agent.
--
-- On the session rather than derived from its messages. A session is what
-- will own a workspace and a sandbox, so "who this is with" is a property of
-- it; and a visibility rule that is an equality on one column is something a
-- reviewer can check at a glance, where an EXISTS over a child table is not.
--
-- Null for group and thread sessions: they have many participants, and naming
-- one of them would be a fact that is not true. Null is also what a direct
-- session falls back to if its user is ever deleted, and the filter treats
-- that as "nobody" rather than "everybody" — see the queries.
ALTER TABLE sessions
    ADD COLUMN user_id uuid REFERENCES users(id) ON DELETE SET NULL;

-- SET NULL rather than CASCADE: deleting a person should not silently delete
-- the record of conversations they had. What survives is a session nobody but
-- a moderator can read, which is the safe direction.

-- A group session with a participant is a contradiction — it would read as
-- "this conversation belongs to one person" while several people speak in it.
-- A direct session without one is allowed, because that is what a deleted
-- user leaves behind.
ALTER TABLE sessions
    ADD CONSTRAINT sessions_participant_only_when_direct
        CHECK (user_id IS NULL OR kind = 'direct');

-- Listing a person's own direct sessions, which is the common read now that
-- the team-wide list filters by it.
CREATE INDEX sessions_participant_idx ON sessions (user_id, last_message_at DESC)
    WHERE user_id IS NOT NULL;

-- +goose Down
DROP INDEX sessions_participant_idx;

ALTER TABLE sessions
    DROP CONSTRAINT sessions_participant_only_when_direct,
    DROP COLUMN user_id;
