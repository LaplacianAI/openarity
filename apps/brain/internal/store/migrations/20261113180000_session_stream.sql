-- +goose Up
SET lock_timeout = '3s';

-- A session's stream is its messages and the events of every run in it, in
-- one order. session_seq is that order: taken from the session's counter
-- under its row lock, so numbers are handed out in the order the writes
-- commit and a reader resuming after n never skips one that commits later.
-- Rows written before this migration have none until `brain backfill
-- session-seq` numbers them, counting down to 0.
ALTER TABLE sessions ADD COLUMN event_seq bigint NOT NULL DEFAULT 0;

ALTER TABLE run_events
    ADD COLUMN session_id uuid REFERENCES sessions(id) ON DELETE CASCADE,
    ADD COLUMN session_seq bigint;

-- asks is a message that asked for an answer: it started a run, or was
-- steered into one. consumed_by is the run that took it.
ALTER TABLE messages
    ADD COLUMN session_seq bigint,
    ADD COLUMN asks boolean NOT NULL DEFAULT false,
    ADD COLUMN consumed_by uuid REFERENCES runs(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX run_events_session_seq_key ON run_events (session_id, session_seq)
    WHERE session_seq IS NOT NULL;
CREATE UNIQUE INDEX messages_session_seq_key ON messages (session_id, session_seq)
    WHERE session_seq IS NOT NULL;
CREATE INDEX messages_session_pending_idx ON messages (session_id, session_seq)
    WHERE asks AND consumed_by IS NULL;

-- Every writer takes the session's row lock before any run's. That is the one
-- lock order, and these triggers lean on it: by the time a row is inserted
-- its session is already held, and the update below only bumps the counter.
-- +goose StatementBegin
CREATE FUNCTION run_events_session_seq() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT session_id INTO NEW.session_id FROM runs WHERE id = NEW.run_id;
    UPDATE sessions SET event_seq = event_seq + 1
    WHERE id = NEW.session_id
    RETURNING event_seq INTO NEW.session_seq;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION messages_session_seq() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE sessions SET event_seq = event_seq + 1
    WHERE id = NEW.session_id
    RETURNING event_seq INTO NEW.session_seq;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER run_events_session_seq BEFORE INSERT ON run_events
    FOR EACH ROW EXECUTE FUNCTION run_events_session_seq();
CREATE TRIGGER messages_session_seq BEFORE INSERT ON messages
    FOR EACH ROW EXECUTE FUNCTION messages_session_seq();

-- A message taken by a run, recorded on that run.
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed')) NOT VALID;

-- The dashboard is a channel: what a person types there is a message, as on
-- Telegram. Every team has exactly one, which nobody creates or deletes, so it
-- takes no part in the name rule: a team may already call a channel of its
-- own "Dashboard".
DROP INDEX channels_team_name_key;
CREATE UNIQUE INDEX channels_team_name_key ON channels (team_id, lower(name))
    WHERE provider <> 'dashboard';
CREATE UNIQUE INDEX channels_team_dashboard_key ON channels (team_id)
    WHERE provider = 'dashboard';

INSERT INTO channels (team_id, provider, name, notify_strangers)
SELECT id, 'dashboard', 'Dashboard', false FROM teams;

-- +goose StatementBegin
CREATE FUNCTION teams_dashboard_channel() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO channels (team_id, provider, name, notify_strangers)
    VALUES (NEW.id, 'dashboard', 'Dashboard', false);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER teams_dashboard_channel AFTER INSERT ON teams
    FOR EACH ROW EXECUTE FUNCTION teams_dashboard_channel();

-- A dashboard channel holds no secret, so its going leaves none to destroy.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION channels_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/channels/' || id, team_id FROM deleted_rows
    WHERE provider <> 'dashboard'
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '3s';

DROP TRIGGER teams_dashboard_channel ON teams;
DROP FUNCTION teams_dashboard_channel();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION channels_record_deleted_secret() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/channels/' || id, team_id FROM deleted_rows
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Its sessions, their messages and their runs go with it.
DELETE FROM runs WHERE session_id IN (
    SELECT s.id FROM sessions s JOIN channels c ON c.id = s.channel_id WHERE c.provider = 'dashboard');
DELETE FROM channels WHERE provider = 'dashboard';
DROP INDEX channels_team_dashboard_key;
DROP INDEX channels_team_name_key;
CREATE UNIQUE INDEX channels_team_name_key ON channels (team_id, lower(name));

DELETE FROM run_events WHERE kind = 'consumed';
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up')) NOT VALID;

DROP TRIGGER messages_session_seq ON messages;
DROP TRIGGER run_events_session_seq ON run_events;
DROP FUNCTION messages_session_seq();
DROP FUNCTION run_events_session_seq();

DROP INDEX messages_session_pending_idx;
DROP INDEX messages_session_seq_key;
DROP INDEX run_events_session_seq_key;

ALTER TABLE messages DROP COLUMN consumed_by, DROP COLUMN asks, DROP COLUMN session_seq;
ALTER TABLE run_events DROP COLUMN session_seq, DROP COLUMN session_id;
ALTER TABLE sessions DROP COLUMN event_seq;
