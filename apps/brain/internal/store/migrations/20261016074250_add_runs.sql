-- +goose Up
SET lock_timeout = '3s';

CREATE TABLE runs (
    id                  uuid PRIMARY KEY,
    team_id             uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    session_id          uuid NOT NULL,
    parent_run_id       uuid REFERENCES runs(id) ON DELETE CASCADE,
    root_run_id         uuid NOT NULL,
    parent_tool_call    text,
    depth               integer NOT NULL DEFAULT 0,
    agent_id            uuid REFERENCES agents(id) ON DELETE SET NULL,
    agent_name          text NOT NULL,
    user_id             uuid REFERENCES users(id) ON DELETE SET NULL,
    status              text NOT NULL DEFAULT 'queued',
    input               text NOT NULL,
    output              text,
    error               text,
    input_tokens        bigint NOT NULL DEFAULT 0,
    output_tokens       bigint NOT NULL DEFAULT 0,
    worker              text,
    heartbeat_at        timestamptz,
    cancel_requested_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,

    CONSTRAINT runs_session_in_team
        FOREIGN KEY (session_id, team_id) REFERENCES sessions (id, team_id) ON DELETE CASCADE,
    CONSTRAINT runs_status_known CHECK (
        status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT runs_input_present CHECK (input <> ''),
    CONSTRAINT runs_agent_name_present CHECK (agent_name <> ''),
    -- A root run is its own root, at depth 0, created by no tool call; a
    -- delegate is none of those. One shape per kind of run.
    CONSTRAINT runs_root_shape CHECK (
        (parent_run_id IS NULL) = (root_run_id = id)
        AND (parent_run_id IS NULL) = (depth = 0)
        AND (parent_run_id IS NULL) = (parent_tool_call IS NULL)),
    CONSTRAINT runs_depth_nonneg CHECK (depth >= 0),
    CONSTRAINT runs_finished_when_terminal CHECK (
        (status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    CONSTRAINT runs_error_when_failed CHECK ((status = 'failed') = (error IS NOT NULL)),
    CONSTRAINT runs_held_while_running CHECK (
        status <> 'running' OR (worker IS NOT NULL AND heartbeat_at IS NOT NULL)),
    CONSTRAINT runs_tokens_nonneg CHECK (input_tokens >= 0 AND output_tokens >= 0)
);

-- A parent that resumes and repeats a delegate call finds the child it made.
CREATE UNIQUE INDEX runs_parent_tool_call_key
    ON runs (parent_run_id, parent_tool_call) WHERE parent_run_id IS NOT NULL;
CREATE INDEX runs_session_recent_idx ON runs (session_id, created_at DESC);
-- The sweep reads only unfinished runs.
CREATE INDEX runs_unfinished_idx ON runs (status, heartbeat_at)
    WHERE status IN ('queued', 'running');
-- The SET NULLs on agent and user delete would otherwise scan.
CREATE INDEX runs_agent_idx ON runs (agent_id);
CREATE INDEX runs_user_idx ON runs (user_id);

CREATE TABLE run_events (
    run_id     uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq        integer NOT NULL,
    kind       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (run_id, seq),
    CONSTRAINT run_events_seq_positive CHECK (seq > 0),
    CONSTRAINT run_events_kind_known CHECK (kind IN (
        'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
        'tool_unavailable', 'finished')),
    CONSTRAINT run_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

-- The SDK's session tables, as sdk/agent/stores/postgres.Schema declares them.
-- The session is the run id as text.
CREATE TABLE agent_messages (
    session  text NOT NULL,
    position int  NOT NULL,
    body     json NOT NULL,
    PRIMARY KEY (session, position)
);

CREATE TABLE agent_steers (
    id      bigserial PRIMARY KEY,
    session text NOT NULL,
    text    text NOT NULL
);

CREATE INDEX agent_steers_session ON agent_steers (session, id);

-- A transcript has no foreign key: the SDK keys it by a string. Deleting a
-- run, including through a team's cascade, must not leave a conversation behind.
-- +goose StatementBegin
CREATE FUNCTION runs_forget_transcripts() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM agent_messages WHERE session IN (SELECT id::text FROM gone);
    DELETE FROM agent_steers   WHERE session IN (SELECT id::text FROM gone);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER runs_forget_transcripts
    AFTER DELETE ON runs
    REFERENCING OLD TABLE AS gone
    FOR EACH STATEMENT
    EXECUTE FUNCTION runs_forget_transcripts();

-- +goose Down
DROP TRIGGER runs_forget_transcripts ON runs;
DROP FUNCTION runs_forget_transcripts();
DROP TABLE agent_steers;
DROP TABLE agent_messages;
DROP TABLE run_events;
DROP TABLE runs;
