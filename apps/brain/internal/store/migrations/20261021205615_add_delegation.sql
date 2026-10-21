-- +goose Up
SET lock_timeout = '3s';

-- Whom an agent may hand work to with the delegate tool: any other agent in
-- its team, only those listed in agent_delegates, or no one. An agent set to
-- none is not given the tool at all.
ALTER TABLE agents ADD COLUMN delegation text NOT NULL DEFAULT 'team';
ALTER TABLE agents ADD CONSTRAINT agents_delegation_known
    CHECK (delegation IN ('team', 'listed', 'none')) NOT VALID;

-- Read only when the agent's delegation is listed. Same team at both ends by
-- composite key, as the grants are; deleting either agent drops the listing.
CREATE TABLE agent_delegates (
    agent_id    uuid NOT NULL,
    delegate_id uuid NOT NULL,
    team_id     uuid NOT NULL,

    PRIMARY KEY (agent_id, delegate_id),
    CONSTRAINT agent_delegates_agent_in_team
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id)
        ON DELETE CASCADE,
    CONSTRAINT agent_delegates_delegate_in_team
        FOREIGN KEY (delegate_id, team_id) REFERENCES agents (id, team_id)
        ON DELETE CASCADE,
    CONSTRAINT agent_delegates_not_self CHECK (agent_id <> delegate_id)
);

-- Deleting an agent checks the listings that name it as a delegate.
CREATE INDEX agent_delegates_delegate_idx ON agent_delegates (delegate_id, team_id);

-- A listing is an edge of the listing agent, so the agent is reprojected.
CREATE TRIGGER agent_delegates_graph_outbox_insert AFTER INSERT ON agent_delegates
    REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();
CREATE TRIGGER agent_delegates_graph_outbox_delete AFTER DELETE ON agent_delegates
    REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT
    EXECUTE FUNCTION graph_outbox_enqueue_agent();

-- +goose Down
SET lock_timeout = '3s';

DROP TABLE agent_delegates;
ALTER TABLE agents DROP COLUMN delegation;
