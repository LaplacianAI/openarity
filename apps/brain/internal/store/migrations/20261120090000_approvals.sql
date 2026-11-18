-- +goose Up
SET LOCAL lock_timeout = '3s';

-- Tool-call approvals: a run can be made to ask a person before a call, and
-- wait for the answer without holding a worker. Nothing reads any of this
-- yet; the gate (internal/runtime) and the policy (internal/approval) come in
-- the slices after this one, and with approval_mode 'off' and no rules a run
-- behaves exactly as before.

-- What a server says of its own tools (readOnlyHint, destructiveHint, ...),
-- as MCP's ToolAnnotations put it on the wire. A hint is believed only when
-- an admin trusts the server's hints: a hostile server's readOnlyHint would
-- otherwise be a way past every approval.
ALTER TABLE mcp_tools ADD COLUMN annotations jsonb;
ALTER TABLE mcp_servers ADD COLUMN trust_hints boolean NOT NULL DEFAULT false;

-- 'off' for every team until the gate exists and an admin can see it.
ALTER TABLE team_settings
    ADD COLUMN approval_mode text NOT NULL DEFAULT 'off',
    ADD COLUMN approval_ttl_minutes integer NOT NULL DEFAULT 60,
    ADD CONSTRAINT team_settings_approval_mode_known CHECK (
        approval_mode IN ('off', 'writes', 'destructive', 'all')) NOT VALID,
    ADD CONSTRAINT team_settings_approval_ttl_range CHECK (
        approval_ttl_minutes BETWEEN 5 AND 1440) NOT VALID;

-- A run waiting for a decision is not finished and holds no worker: it is
-- saved, its workflow ends, and a decision queues it again under the next
-- attempt's workflow id.
ALTER TABLE runs
    ADD COLUMN attempt integer NOT NULL DEFAULT 0,
    ADD COLUMN unattended boolean NOT NULL DEFAULT false,
    ADD COLUMN waited_seconds integer NOT NULL DEFAULT 0,
    ADD COLUMN waiting_since timestamptz;
ALTER TABLE runs DROP CONSTRAINT runs_status_known;
ALTER TABLE runs
    ADD CONSTRAINT runs_status_known CHECK (
        status IN ('queued', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')) NOT VALID,
    ADD CONSTRAINT runs_waiting_shape CHECK (
        status <> 'waiting'
        OR (worker IS NULL AND heartbeat_at IS NULL AND waiting_since IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT runs_attempt_nonneg CHECK (attempt >= 0 AND waited_seconds >= 0) NOT VALID;

ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning',
    'approval_requested', 'approval_decided', 'waiting')) NOT VALID;

-- A team's standing answers: for one tool, or for every tool of a server.
-- The most specific rule wins over the team's mode; require_other means the
-- person a call is for may not approve it themselves.
CREATE TABLE approval_rules (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    target_kind   text NOT NULL,
    target        text NOT NULL,
    mode          text NOT NULL,
    require_other boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT approval_rules_target_kind_known CHECK (target_kind IN ('tool', 'server')),
    CONSTRAINT approval_rules_target_present CHECK (target <> ''),
    CONSTRAINT approval_rules_mode_known CHECK (mode IN ('allow', 'ask', 'deny'))
);
CREATE UNIQUE INDEX approval_rules_target_key
    ON approval_rules (team_id, target_kind, lower(target));

-- One call a run asked about. (run_id, call_id) makes asking idempotent: a
-- gate retried after a crash finds the row it wrote. An approval is used at
-- most once, and used_at is what says so.
CREATE TABLE approvals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id        uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    run_id         uuid NOT NULL,
    root_run_id    uuid NOT NULL,
    call_id        text NOT NULL,
    tool           text NOT NULL,
    class          text NOT NULL,
    arguments      jsonb NOT NULL DEFAULT '{}',
    summary        text NOT NULL,
    status         text NOT NULL DEFAULT 'pending',
    code           text NOT NULL,
    requested_for  uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    decided_via    text,
    decision_scope text,
    reason         text,
    expires_at     timestamptz NOT NULL,
    decided_at     timestamptz,
    used_at        timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT approvals_run_in_team
        FOREIGN KEY (run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT approvals_root_in_team
        FOREIGN KEY (root_run_id, team_id) REFERENCES runs (id, team_id) ON DELETE CASCADE,
    CONSTRAINT approvals_call_key UNIQUE (run_id, call_id),
    CONSTRAINT approvals_call_present CHECK (call_id <> '' AND tool <> ''),
    CONSTRAINT approvals_class_known CHECK (class IN ('read', 'write', 'destructive')),
    CONSTRAINT approvals_status_known CHECK (
        status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')),
    -- Four characters a person types back into a chat, from an alphabet
    -- without the letters that read as digits (I, O).
    CONSTRAINT approvals_code_shape CHECK (code ~ '^[A-HJ-NP-Z2-9]{4}$'),
    CONSTRAINT approvals_via_known CHECK (
        decided_via IS NULL OR decided_via IN ('dashboard', 'channel', 'cli', 'sweep')),
    CONSTRAINT approvals_scope_known CHECK (
        decision_scope IS NULL OR decision_scope IN ('once', 'run')),
    CONSTRAINT approvals_decided_when_decided CHECK (
        (status = 'pending') = (decided_at IS NULL)),
    CONSTRAINT approvals_used_after_approved CHECK (
        used_at IS NULL OR status = 'approved')
);
CREATE INDEX approvals_pending_idx ON approvals (team_id, created_at)
    WHERE status = 'pending';
CREATE INDEX approvals_run_idx ON approvals (run_id);
CREATE INDEX approvals_root_idx ON approvals (root_run_id);
-- Among what one run tree is waiting on, a code names one request, so a reply
-- in the chat finds it without a scan.
CREATE UNIQUE INDEX approvals_pending_code_key ON approvals (root_run_id, code)
    WHERE status = 'pending';

-- +goose Down
SET LOCAL lock_timeout = '3s';

DROP TABLE approvals;
DROP TABLE approval_rules;

DELETE FROM run_events WHERE kind IN ('approval_requested', 'approval_decided', 'waiting');
ALTER TABLE run_events DROP CONSTRAINT run_events_kind_known;
ALTER TABLE run_events ADD CONSTRAINT run_events_kind_known CHECK (kind IN (
    'started', 'resumed', 'step', 'tool_call', 'tool_result', 'usage',
    'tool_unavailable', 'finished', 'delegated', 'plan', 'proposed', 'text',
    'compacted', 'wrap_up', 'consumed', 'reasoning')) NOT VALID;

-- A parked run cannot be resumed without the gate, so it ends as cancelled.
UPDATE runs SET status = 'cancelled', finished_at = now(), waiting_since = NULL
WHERE status = 'waiting';
ALTER TABLE runs
    DROP CONSTRAINT runs_attempt_nonneg,
    DROP CONSTRAINT runs_waiting_shape,
    DROP CONSTRAINT runs_status_known;
ALTER TABLE runs ADD CONSTRAINT runs_status_known CHECK (
    status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')) NOT VALID;
ALTER TABLE runs
    DROP COLUMN waiting_since,
    DROP COLUMN waited_seconds,
    DROP COLUMN unattended,
    DROP COLUMN attempt;

ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_approval_ttl_range,
    DROP CONSTRAINT team_settings_approval_mode_known,
    DROP COLUMN approval_ttl_minutes,
    DROP COLUMN approval_mode;

ALTER TABLE mcp_servers DROP COLUMN trust_hints;
ALTER TABLE mcp_tools DROP COLUMN annotations;
