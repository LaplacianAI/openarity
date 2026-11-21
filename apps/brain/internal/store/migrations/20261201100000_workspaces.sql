-- +goose Up
SET LOCAL lock_timeout = '3s';

-- An agent granted the workspace is given Bash, Read, Write, Edit, Glob and
-- Grep, in a room of its session's, when its team has a sandbox. A grant of
-- its own, as browser is: a shell is more than any one tool.
ALTER TABLE agents ADD COLUMN workspace boolean NOT NULL DEFAULT false;

-- A session is the second thing a room is owned by: one room per session,
-- shared by every run on it. The migration is one transaction, so the table
-- is locked from the drop to the commit and every row is checked under it;
-- sandbox_rooms holds a row per room in use, so that is brief.
ALTER TABLE sandbox_rooms DROP CONSTRAINT sandbox_rooms_owner_kind_known;
ALTER TABLE sandbox_rooms ADD CONSTRAINT sandbox_rooms_owner_kind_known
    CHECK (owner_kind IN ('mcp_server', 'session'));

-- A session deleted, on its own or with its team, takes its room, as a
-- command MCP server does: the rows going is what makes the tombstone the
-- sweep deletes the room and its token by.
-- +goose StatementBegin
CREATE FUNCTION sessions_drop_rooms() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM sandbox_rooms
    WHERE owner_kind = 'session'
      AND owner_id IN (SELECT id FROM deleted_rows);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sessions_tombstone_rooms
    AFTER DELETE ON sessions
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION sessions_drop_rooms();

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TRIGGER sessions_tombstone_rooms ON sessions;
DROP FUNCTION sessions_drop_rooms();

-- Sessions' rooms go as rooms always go: their rows deleted, which
-- tombstones each room and its token for the sweep.
DELETE FROM sandbox_rooms WHERE owner_kind = 'session';

ALTER TABLE sandbox_rooms DROP CONSTRAINT sandbox_rooms_owner_kind_known;
ALTER TABLE sandbox_rooms ADD CONSTRAINT sandbox_rooms_owner_kind_known
    CHECK (owner_kind IN ('mcp_server'));

ALTER TABLE agents DROP COLUMN workspace;
