-- +goose Up
SET lock_timeout = '3s';

-- The graph is behind until a row here is drained. Postgres and FalkorDB
-- cannot share a transaction, so what commits with the change is the record
-- that the graph needs catching up, not the graph write itself.
--
-- A row names what changed, never what it changed to: the projector reads the
-- current state, so replaying a row or draining out of order is harmless.
--
-- No foreign keys, on purpose: the commonest reason to write a row is that
-- the thing it names was just deleted.
CREATE TABLE graph_outbox (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    uuid NOT NULL,
    entity     text NOT NULL,
    entity_id  uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- A row that keeps failing must not starve the rest, and must be visible.
    -- The same pair, for the same reason, as deleted_objects.
    attempts        integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,

    CONSTRAINT graph_outbox_entity_known CHECK (entity IN
        ('team', 'agent', 'mcp_server', 'mcp_tool', 'skill'))
);

-- Never-tried rows first, then oldest; a failing row sinks behind fresh work.
CREATE INDEX graph_outbox_drain_idx ON graph_outbox (last_attempt_at NULLS FIRST, id);

-- Every trigger below names its transition table changed_rows, whichever
-- event it fires on, so one function serves insert, update and delete.
-- Postgres allows a transition table only on a single-event trigger, which is
-- why there are three triggers per table rather than one.

-- +goose StatementBegin
CREATE FUNCTION graph_outbox_enqueue() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph_outbox (team_id, entity, entity_id)
    SELECT DISTINCT team_id, TG_ARGV[0], id FROM changed_rows;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- A grant is part of its agent's node, so a changed grant re-projects the agent.
-- +goose StatementBegin
CREATE FUNCTION graph_outbox_enqueue_agent() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph_outbox (team_id, entity, entity_id)
    SELECT DISTINCT team_id, 'agent', agent_id FROM changed_rows;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- A team is its own graph, so only its deletion matters: that drops the graph.
-- +goose StatementBegin
CREATE FUNCTION graph_outbox_enqueue_team() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph_outbox (team_id, entity, entity_id)
    SELECT id, 'team', id FROM changed_rows;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agents_graph_outbox_insert AFTER INSERT ON agents
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('agent');
CREATE TRIGGER agents_graph_outbox_update AFTER UPDATE ON agents
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('agent');
CREATE TRIGGER agents_graph_outbox_delete AFTER DELETE ON agents
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('agent');

CREATE TRIGGER mcp_servers_graph_outbox_insert AFTER INSERT ON mcp_servers
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_server');
CREATE TRIGGER mcp_servers_graph_outbox_update AFTER UPDATE ON mcp_servers
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_server');
CREATE TRIGGER mcp_servers_graph_outbox_delete AFTER DELETE ON mcp_servers
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_server');

CREATE TRIGGER mcp_tools_graph_outbox_insert AFTER INSERT ON mcp_tools
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_tool');
CREATE TRIGGER mcp_tools_graph_outbox_update AFTER UPDATE ON mcp_tools
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_tool');
CREATE TRIGGER mcp_tools_graph_outbox_delete AFTER DELETE ON mcp_tools
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('mcp_tool');

CREATE TRIGGER skills_graph_outbox_insert AFTER INSERT ON skills
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('skill');
CREATE TRIGGER skills_graph_outbox_update AFTER UPDATE ON skills
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('skill');
CREATE TRIGGER skills_graph_outbox_delete AFTER DELETE ON skills
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue('skill');

CREATE TRIGGER agent_mcp_servers_graph_outbox_insert AFTER INSERT ON agent_mcp_servers
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();
CREATE TRIGGER agent_mcp_servers_graph_outbox_update AFTER UPDATE ON agent_mcp_servers
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();
CREATE TRIGGER agent_mcp_servers_graph_outbox_delete AFTER DELETE ON agent_mcp_servers
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();

CREATE TRIGGER agent_skills_graph_outbox_insert AFTER INSERT ON agent_skills
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();
CREATE TRIGGER agent_skills_graph_outbox_delete AFTER DELETE ON agent_skills
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();

CREATE TRIGGER teams_graph_outbox_delete AFTER DELETE ON teams
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_team();

-- +goose Down
DROP TRIGGER teams_graph_outbox_delete ON teams;

DROP TRIGGER agent_skills_graph_outbox_delete ON agent_skills;
DROP TRIGGER agent_skills_graph_outbox_insert ON agent_skills;

DROP TRIGGER agent_mcp_servers_graph_outbox_delete ON agent_mcp_servers;
DROP TRIGGER agent_mcp_servers_graph_outbox_update ON agent_mcp_servers;
DROP TRIGGER agent_mcp_servers_graph_outbox_insert ON agent_mcp_servers;

DROP TRIGGER skills_graph_outbox_delete ON skills;
DROP TRIGGER skills_graph_outbox_update ON skills;
DROP TRIGGER skills_graph_outbox_insert ON skills;

DROP TRIGGER mcp_tools_graph_outbox_delete ON mcp_tools;
DROP TRIGGER mcp_tools_graph_outbox_update ON mcp_tools;
DROP TRIGGER mcp_tools_graph_outbox_insert ON mcp_tools;

DROP TRIGGER mcp_servers_graph_outbox_delete ON mcp_servers;
DROP TRIGGER mcp_servers_graph_outbox_update ON mcp_servers;
DROP TRIGGER mcp_servers_graph_outbox_insert ON mcp_servers;

DROP TRIGGER agents_graph_outbox_delete ON agents;
DROP TRIGGER agents_graph_outbox_update ON agents;
DROP TRIGGER agents_graph_outbox_insert ON agents;

DROP FUNCTION graph_outbox_enqueue_team();
DROP FUNCTION graph_outbox_enqueue_agent();
DROP FUNCTION graph_outbox_enqueue();

DROP TABLE graph_outbox;
