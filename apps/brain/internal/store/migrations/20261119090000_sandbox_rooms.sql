-- +goose Up
SET lock_timeout = '3s';

-- A room a team's sandbox made for something that needs one to run in. The
-- first owner is a command MCP server; agent code, workspaces and skill
-- scripts will be others, and each is added to owner_kind by its own
-- migration, with the trigger that sends its deletion here. A room is made on
-- the first call that needs it and kept while it is used: the row says which
-- room it is, at which provider, with which key, and what it was made from
-- (spec_hash: the image, the command, the settings and the egress), so an
-- owner edited since is given a fresh room rather than the stale one.
--
-- The row names the provider, its address and the key the room was made
-- with, and not the team's current settings, because the room is deleted
-- with the credentials that made it. A team that changes provider or key
-- cannot be asked to delete a room from the old one with the new one.
--
-- The room's own bearer token is a secret at
-- teams/<team>/sandbox/rooms/<id>. The id is this row's, so a room replaced
-- by a new row has a new token, and the old one is tombstoned with the old
-- row without the sweep ever deleting one a live room uses.
CREATE TABLE sandbox_rooms (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,

    owner_kind text NOT NULL,
    owner_id   uuid NOT NULL,

    provider          text NOT NULL,
    provider_url      text,
    room_id           text NOT NULL,
    spec_hash         text NOT NULL,
    sandbox_secret_id uuid NOT NULL,

    last_used_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- One room per owner. The owner is polymorphic, so there is no foreign
    -- key to hold it: each owner kind's deletion reaches this table through
    -- a trigger on the owner's own table, below.
    CONSTRAINT sandbox_rooms_one_per_owner UNIQUE (owner_kind, owner_id),
    CONSTRAINT sandbox_rooms_owner_kind_known CHECK (owner_kind IN ('mcp_server')),
    CONSTRAINT sandbox_rooms_provider_known CHECK (provider IN ('e2b', 'daytona', 'sandboxd')),
    CONSTRAINT sandbox_rooms_room_present CHECK (room_id <> '')
);

CREATE INDEX sandbox_rooms_team_idx ON sandbox_rooms (team_id);
CREATE INDEX sandbox_rooms_idle_idx ON sandbox_rooms (last_used_at);
CREATE INDEX sandbox_rooms_key_idx ON sandbox_rooms (sandbox_secret_id);

-- A room whose row is gone and which the provider may still have. A row here
-- is erasure outstanding, as in deleted_objects and deleted_secrets, and it
-- carries what deleting the room takes: where, and the id of the key to ask
-- with. It holds no secret itself.
--
-- team_id has no foreign key for the reason deleted_objects has none: the
-- commonest cause of a tombstone is the team being deleted.
CREATE TABLE deleted_rooms (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL,

    provider          text NOT NULL,
    provider_url      text,
    room_id           text NOT NULL,
    sandbox_secret_id uuid NOT NULL,

    deleted_at      timestamptz NOT NULL DEFAULT now(),
    attempts        integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,

    CONSTRAINT deleted_rooms_room_present CHECK (room_id <> '')
);

CREATE INDEX deleted_rooms_sweep_idx
    ON deleted_rooms (last_attempt_at NULLS FIRST, deleted_at);
CREATE INDEX deleted_rooms_key_idx ON deleted_rooms (sandbox_secret_id);

-- A row deleted, by a cascade, by the sweep of idle rooms or by a room being
-- replaced, leaves its room and its token to delete. The secret path is built
-- here as secrets.RoomPath builds it, and a test holds the two to each other.
-- +goose StatementBegin
CREATE FUNCTION sandbox_rooms_record_deleted() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO deleted_rooms (team_id, provider, provider_url, room_id, sandbox_secret_id)
    SELECT team_id, provider, provider_url, room_id, sandbox_secret_id FROM deleted_rows;

    INSERT INTO deleted_secrets (path, team_id)
    SELECT 'teams/' || team_id || '/sandbox/rooms/' || id, team_id FROM deleted_rows
    ON CONFLICT (path) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sandbox_rooms_tombstone_deleted
    AFTER DELETE ON sandbox_rooms
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION sandbox_rooms_record_deleted();

-- A command MCP server deleted, on its own or with its team, takes its room
-- with it. The room is at a provider no cascade reaches, so the rows going is
-- what turns it into a tombstone for the sweep.
-- +goose StatementBegin
CREATE FUNCTION mcp_servers_drop_rooms() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM sandbox_rooms
    WHERE owner_kind = 'mcp_server'
      AND owner_id IN (SELECT id FROM deleted_rows);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER mcp_servers_tombstone_rooms
    AFTER DELETE ON mcp_servers
    REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION mcp_servers_drop_rooms();

-- A team that changes its provider, its address or its key no longer owns the
-- rooms made under the old ones: they go the same way, deleted with the key
-- that made them, which waits for them (ClaimDeletedSecrets).
-- +goose StatementBegin
CREATE FUNCTION team_settings_drop_rooms() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM sandbox_rooms r
    USING old_rows o JOIN new_rows n ON n.team_id = o.team_id
    WHERE r.team_id = o.team_id
      AND (o.sandbox_provider IS DISTINCT FROM n.sandbox_provider
        OR o.sandbox_url IS DISTINCT FROM n.sandbox_url
        OR o.sandbox_secret_id IS DISTINCT FROM n.sandbox_secret_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER team_settings_tombstone_rooms
    AFTER UPDATE ON team_settings
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION team_settings_drop_rooms();

-- +goose Down
SET lock_timeout = '3s';

DROP TRIGGER team_settings_tombstone_rooms ON team_settings;
DROP FUNCTION team_settings_drop_rooms();
DROP TRIGGER mcp_servers_tombstone_rooms ON mcp_servers;
DROP FUNCTION mcp_servers_drop_rooms();

-- Rolling back cannot delete a room at a provider, and the key that could
-- goes with the settings' own rollback, so what it can do is delete the
-- tokens, and leave the rooms to expire at their providers.
INSERT INTO deleted_secrets (path, team_id)
SELECT 'teams/' || team_id || '/sandbox/rooms/' || id, team_id
FROM sandbox_rooms
ON CONFLICT (path) DO NOTHING;

DROP TRIGGER sandbox_rooms_tombstone_deleted ON sandbox_rooms;
DROP FUNCTION sandbox_rooms_record_deleted();
DROP TABLE deleted_rooms;
DROP TABLE sandbox_rooms;
