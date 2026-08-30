-- +goose Up
SET lock_timeout = '3s';

-- Deleting an attachment row does not delete its object: a Postgres cascade
-- does not reach a bucket. Every route that deletes a team, a channel or a
-- user already cascades to attachments, so the bytes of a file somebody asked
-- to erase outlive the request that erased it. Measured on this schema before
-- this migration: DELETE FROM teams left the attachment rows gone and the
-- object untouched, with nothing anywhere recording that it existed.
--
-- Postgres and the object store cannot share a transaction, so the two writes
-- cannot both be atomic. What can be atomic is *recording the intent*: the
-- delete and the tombstone commit together, and a sweeper converges the bucket
-- afterwards. That is a transactional outbox, and it makes erasure a thing
-- that has been recorded rather than a thing that was attempted.

-- The team is what selects the key that opens the object, and the sweeper
-- needs it after the session is already gone. It cannot join for it: during a
-- team cascade the sessions row is deleted before the attachments trigger
-- fires, so the join returns null and the tombstone loses the one field the
-- sweeper cannot work without. Measured on 18.6 with a statement-level trigger
-- and a transition table.
--
-- Third copy of the same fact, and the same trade as session_id before it: the
-- composite foreign key below means it cannot drift, so what is left is
-- sixteen bytes.
ALTER TABLE sessions ADD CONSTRAINT sessions_id_team_key UNIQUE (id, team_id);

ALTER TABLE attachments ADD COLUMN team_id uuid;

UPDATE attachments a
SET team_id = s.team_id
FROM sessions s
WHERE s.id = a.session_id;

ALTER TABLE attachments ALTER COLUMN team_id SET NOT NULL;

-- The pair, so an attachment naming a team its session is not in cannot be
-- written. This is a second foreign key onto a different table, not a second
-- one onto messages: (message_id, session_id) says which message, and
-- (session_id, team_id) says which team, and neither implies the other.
ALTER TABLE attachments
    ADD CONSTRAINT attachments_session_in_team
        FOREIGN KEY (session_id, team_id) REFERENCES sessions (id, team_id)
        ON DELETE CASCADE;

-- An object whose row is gone and whose bytes are not. A row here means
-- erasure is outstanding; no row means it is done. That is the audit trail and
-- the alarm: a tombstone that stops shrinking is a sweeper that stopped.
--
-- It holds no personal data on purpose. object_key is a uuid under a team
-- prefix and team_id is a uuid; the filename is deliberately absent, because a
-- table of the names of files people asked to have deleted, kept because they
-- asked, is the thing you failed to erase.
--
-- team_id has no foreign key, and that is the point: the commonest reason a
-- tombstone is written is that the team itself is being deleted, and a
-- reference would cascade away the record of the work still to do.
CREATE TABLE deleted_objects (
    object_key text PRIMARY KEY,
    team_id    uuid NOT NULL,

    deleted_at timestamptz NOT NULL DEFAULT now(),

    -- A sweep that keeps failing on one object must not starve the rest, and
    -- must be visible. Both are this pair's job.
    attempts        integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,

    CONSTRAINT deleted_objects_key_present CHECK (object_key <> '')
);

-- Oldest first, and a failing object sinks. NULLS FIRST puts never-attempted
-- rows ahead of everything already tried.
CREATE INDEX deleted_objects_sweep_idx
    ON deleted_objects (last_attempt_at NULLS FIRST, deleted_at);

-- +goose StatementBegin
CREATE FUNCTION attachments_record_deleted_object() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_objects (object_key, team_id)
    SELECT object_key, team_id FROM deleted_rows
    ON CONFLICT (object_key) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- A trigger rather than a query, because a cascade never runs our SQL. Deleting
-- a team removes its attachments through channels, sessions and messages
-- without any Go code seeing a row, so an outbox written by our own delete
-- statements would catch nothing — which is every deletion that exists today.
--
-- Statement-level with a transition table, not FOR EACH ROW: deleting a team
-- with ten thousand attachments is then one INSERT rather than ten thousand
-- trigger invocations.
--
-- ON CONFLICT DO NOTHING because two rows may name one object once identical
-- files share one. The sweeper asks CountAttachmentsByObjectKey before it
-- deletes anything, so a tombstone for an object another row still needs is
-- dropped rather than acted on.
CREATE TRIGGER attachments_tombstone_deleted_objects
    AFTER DELETE ON attachments
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION attachments_record_deleted_object();

-- The same shape for the other system Postgres cannot reach. Deleting a team
-- leaves the key every one of its attachments was sealed under sitting in the
-- secret store, and deleting a channel leaves its signing secret — today the
-- channel handler deletes that one best-effort, logging when it fails, which
-- is a leak with a log line in front of it. A team cascade skips the handler
-- entirely and leaks every channel it owned.
--
-- Destroying the team key is the half of an erasure that does not wait for a
-- sweep: once it is gone every object of that team is undecryptable, including
-- the copies in bucket backups no sweeper can reach.
--
-- Separate table rather than a kind column on the one above. The two carry
-- different columns today and would carry different constraints tomorrow, and
-- a shared table forces the stricter of two sets of rules onto both.
CREATE TABLE deleted_secrets (
    path    text PRIMARY KEY,
    team_id uuid NOT NULL,

    deleted_at timestamptz NOT NULL DEFAULT now(),

    attempts        integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,

    CONSTRAINT deleted_secrets_path_present CHECK (path <> '')
);

CREATE INDEX deleted_secrets_sweep_idx
    ON deleted_secrets (last_attempt_at NULLS FIRST, deleted_at);

-- The paths are built here rather than passed in, because a cascade has no
-- caller to pass anything. They must match secrets.Path and secrets.TeamPath
-- exactly; a schema test asserts that against the Go, so a change to either
-- side fails rather than silently orphaning every secret written afterwards.
-- +goose StatementBegin
CREATE FUNCTION channels_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/channels/' || id, team_id FROM deleted_rows
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION teams_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || id || '/attachments', id FROM deleted_rows
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER channels_tombstone_deleted_secrets
    AFTER DELETE ON channels
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION channels_record_deleted_secret();

CREATE TRIGGER teams_tombstone_deleted_secrets
    AFTER DELETE ON teams
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION teams_record_deleted_secret();

-- +goose Down
DROP TRIGGER teams_tombstone_deleted_secrets ON teams;

DROP TRIGGER channels_tombstone_deleted_secrets ON channels;

DROP FUNCTION teams_record_deleted_secret();

DROP FUNCTION channels_record_deleted_secret();

DROP TABLE deleted_secrets;

DROP TRIGGER attachments_tombstone_deleted_objects ON attachments;

DROP FUNCTION attachments_record_deleted_object();

DROP TABLE deleted_objects;

ALTER TABLE attachments DROP CONSTRAINT attachments_session_in_team;

ALTER TABLE attachments DROP COLUMN team_id;

ALTER TABLE sessions DROP CONSTRAINT sessions_id_team_key;
