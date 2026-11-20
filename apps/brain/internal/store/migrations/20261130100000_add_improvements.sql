-- +goose Up
SET LOCAL lock_timeout = '3s';

-- Nothing called a learning remains: a team's agents improve their own
-- harness instead, and this switch turns that on.
ALTER TABLE team_settings RENAME COLUMN learning TO improvement;

-- A proposed change to one agent's harness, or to one skill: the target's
-- whole state before and after it. Applying writes after; reverting writes
-- before; either is refused when the target no longer matches.
CREATE TABLE improvement_proposals (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id      uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    -- Whose runs it came from, and the target of every kind but skill_edit.
    agent_id     uuid NOT NULL,
    skill_id     uuid,
    kind         text NOT NULL,
    status       text NOT NULL DEFAULT 'waiting',
    why          text NOT NULL,
    hypothesis   text NOT NULL,
    scope        text NOT NULL,
    signature    text,
    -- The runs it cites. Plain ids: a run's session can be deleted.
    run_ids      uuid[] NOT NULL DEFAULT '{}',
    outsider     boolean NOT NULL DEFAULT false,
    marks        text[] NOT NULL DEFAULT '{}',
    before       jsonb NOT NULL,
    after        jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at   timestamptz,
    reverted_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    reverted_at  timestamptz,
    withdrawn_by uuid REFERENCES improvement_proposals (id) ON DELETE SET NULL,

    CONSTRAINT improvement_proposals_kind_known CHECK (kind IN (
        'instructions', 'description', 'skill_grant', 'skill_drop', 'skill_new', 'skill_edit',
        'server_grant', 'server_drop', 'tools_narrow', 'builtin_tool', 'tool_note',
        'limits', 'delegation', 'pattern')),
    CONSTRAINT improvement_proposals_status_known CHECK (status IN (
        'waiting', 'applied', 'rejected', 'reverted', 'withdrawn')),
    CONSTRAINT improvement_proposals_scope_known CHECK (scope IN ('invariant', 'domain')),
    CONSTRAINT improvement_proposals_skill_target CHECK ((kind = 'skill_edit') = (skill_id IS NOT NULL)),
    CONSTRAINT improvement_proposals_text_present CHECK (why <> '' AND hypothesis <> ''),
    CONSTRAINT improvement_proposals_snapshots_objects CHECK (
        jsonb_typeof(before) = 'object' AND jsonb_typeof(after) = 'object'),
    -- A person reviews every proposal; one this large is not reviewable.
    CONSTRAINT improvement_proposals_size CHECK (
        octet_length(before::text) + octet_length(after::text) <= 2097152),
    -- A person decides approve and reject; a withdrawal is the brain's.
    CONSTRAINT improvement_proposals_decided CHECK (
        (status IN ('waiting', 'withdrawn')) = (decided_at IS NULL)),
    CONSTRAINT improvement_proposals_decided_by_at CHECK (decided_by IS NULL OR decided_at IS NOT NULL),
    CONSTRAINT improvement_proposals_reverted CHECK ((status = 'reverted') = (reverted_at IS NOT NULL)),
    CONSTRAINT improvement_proposals_reverted_by_at CHECK (reverted_by IS NULL OR reverted_at IS NOT NULL),
    CONSTRAINT improvement_proposals_withdrawn CHECK (status = 'withdrawn' OR withdrawn_by IS NULL),
    -- An agent's proposals go with it, and a skill's with the skill.
    CONSTRAINT improvement_proposals_agent
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id) ON DELETE CASCADE,
    CONSTRAINT improvement_proposals_skill
        FOREIGN KEY (skill_id, team_id) REFERENCES skills (id, team_id) ON DELETE CASCADE
);

CREATE INDEX improvement_proposals_page_idx
    ON improvement_proposals (team_id, status, created_at DESC, id DESC);
CREATE INDEX improvement_proposals_agent_idx ON improvement_proposals (agent_id, status);
CREATE INDEX improvement_proposals_skill_idx ON improvement_proposals (skill_id) WHERE skill_id IS NOT NULL;

-- A usage note on one tool for one agent, shown with that tool in its runs.
-- Its own table, not a column of agent_mcp_servers: a person's PUT of the
-- agent clears and rewrites the grants, and must not drop notes it does not
-- mention. A note on a server the agent no longer holds is not shown.
CREATE TABLE agent_tool_notes (
    agent_id      uuid NOT NULL,
    team_id       uuid NOT NULL,
    mcp_server_id uuid NOT NULL,
    tool          text NOT NULL,
    note          text NOT NULL,

    PRIMARY KEY (agent_id, mcp_server_id, tool),
    CONSTRAINT agent_tool_notes_tool_present CHECK (tool <> ''),
    CONSTRAINT agent_tool_notes_note_shaped CHECK (note <> '' AND length(note) <= 1000),
    CONSTRAINT agent_tool_notes_agent
        FOREIGN KEY (agent_id, team_id) REFERENCES agents (id, team_id) ON DELETE CASCADE,
    CONSTRAINT agent_tool_notes_server
        FOREIGN KEY (mcp_server_id, team_id) REFERENCES mcp_servers (id, team_id) ON DELETE CASCADE
);

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE agent_tool_notes;
DROP TABLE improvement_proposals;
ALTER TABLE team_settings RENAME COLUMN improvement TO learning;
