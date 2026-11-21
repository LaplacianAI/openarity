-- name: GetTeamSettings :one
-- No row means the team never saved: the caller reads that as the defaults.
SELECT * FROM team_settings WHERE team_id = $1;

-- name: UpsertTeamSettings :one
-- The whole row every time. A save that keeps the search key passes the same
-- search_secret_id back, and the trigger records nothing; a different one
-- tombstones the old key in this statement's transaction.
INSERT INTO team_settings (
    team_id,
    model_name, model_max_tokens, model_temperature,
    search_provider, search_url, search_secret_id,
    improvement, compaction_messages, compaction_tokens_min, compaction_tokens_max, hub_agent_id,
    sandbox_provider, sandbox_url, sandbox_secret_id, sandbox_template,
    browser_provider, browser_url, browser_secret_id,
    judge_model_name, judge_model_max_tokens, judge_model_temperature,
    improvement_sample_share, improvement_daily_passes, improvement_pass_tokens, improvement_pass_minutes,
    improvement_since,
    sandbox_keep_hours, sandbox_idle_minutes,
    sandbox_workspace_memory_mb, sandbox_workspace_cpus, sandbox_workspace_disk_mb
) VALUES (
    sqlc.arg(team_id),
    sqlc.narg(model_name), sqlc.narg(model_max_tokens), sqlc.narg(model_temperature),
    sqlc.arg(search_provider), sqlc.narg(search_url), sqlc.narg(search_secret_id),
    sqlc.arg(improvement), sqlc.arg(compaction_messages),
    sqlc.arg(compaction_tokens_min), sqlc.arg(compaction_tokens_max), sqlc.narg(hub_agent_id),
    sqlc.arg(sandbox_provider), sqlc.narg(sandbox_url), sqlc.narg(sandbox_secret_id), sqlc.narg(sandbox_template),
    sqlc.arg(browser_provider), sqlc.narg(browser_url), sqlc.narg(browser_secret_id),
    sqlc.narg(judge_model_name), sqlc.narg(judge_model_max_tokens), sqlc.narg(judge_model_temperature),
    -- Null keeps what is stored, or the default on a first save.
    sqlc.arg(improvement_sample_share), COALESCE(sqlc.narg(improvement_daily_passes)::smallint, 20),
    COALESCE(sqlc.narg(improvement_pass_tokens)::integer, 400000),
    COALESCE(sqlc.narg(improvement_pass_minutes)::smallint, 20),
    CASE WHEN sqlc.arg(improvement)::bool THEN now() END,
    -- The caller sends what is stored for these when a save leaves them out:
    -- a null idle or size is the provider's, not "keep".
    COALESCE(sqlc.narg(sandbox_keep_hours)::integer, 24), sqlc.narg(sandbox_idle_minutes),
    sqlc.narg(sandbox_workspace_memory_mb), sqlc.narg(sandbox_workspace_cpus), sqlc.narg(sandbox_workspace_disk_mb)
)
ON CONFLICT (team_id) DO UPDATE SET
    model_name          = EXCLUDED.model_name,
    model_max_tokens    = EXCLUDED.model_max_tokens,
    model_temperature   = EXCLUDED.model_temperature,
    search_provider     = EXCLUDED.search_provider,
    search_url          = EXCLUDED.search_url,
    search_secret_id    = EXCLUDED.search_secret_id,
    improvement         = EXCLUDED.improvement,
    compaction_messages = EXCLUDED.compaction_messages,
    compaction_tokens_min = EXCLUDED.compaction_tokens_min,
    compaction_tokens_max = EXCLUDED.compaction_tokens_max,
    hub_agent_id        = EXCLUDED.hub_agent_id,
    sandbox_provider    = EXCLUDED.sandbox_provider,
    sandbox_url         = EXCLUDED.sandbox_url,
    sandbox_secret_id   = EXCLUDED.sandbox_secret_id,
    sandbox_template    = EXCLUDED.sandbox_template,
    browser_provider    = EXCLUDED.browser_provider,
    browser_url         = EXCLUDED.browser_url,
    browser_secret_id   = EXCLUDED.browser_secret_id,
    judge_model_name        = EXCLUDED.judge_model_name,
    judge_model_max_tokens  = EXCLUDED.judge_model_max_tokens,
    judge_model_temperature = EXCLUDED.judge_model_temperature,
    improvement_sample_share = EXCLUDED.improvement_sample_share,
    sandbox_keep_hours          = EXCLUDED.sandbox_keep_hours,
    sandbox_idle_minutes        = EXCLUDED.sandbox_idle_minutes,
    sandbox_workspace_memory_mb = EXCLUDED.sandbox_workspace_memory_mb,
    sandbox_workspace_cpus      = EXCLUDED.sandbox_workspace_cpus,
    sandbox_workspace_disk_mb   = EXCLUDED.sandbox_workspace_disk_mb,
    improvement_daily_passes = COALESCE(sqlc.narg(improvement_daily_passes)::smallint, team_settings.improvement_daily_passes),
    improvement_pass_tokens  = COALESCE(sqlc.narg(improvement_pass_tokens)::integer, team_settings.improvement_pass_tokens),
    improvement_pass_minutes = COALESCE(sqlc.narg(improvement_pass_minutes)::smallint, team_settings.improvement_pass_minutes),
    -- Set each time the switch goes on, kept while it stays on: runs from
    -- before it went on are never judged.
    improvement_since   = CASE
        WHEN EXCLUDED.improvement AND NOT team_settings.improvement THEN now()
        ELSE team_settings.improvement_since END,
    updated_at          = now()
RETURNING *;

-- name: RecordDeletedSecret :exec
-- A key that reached the secret store and then lost its save: recorded so the
-- sweep removes it, rather than a credential nothing names.
INSERT INTO deleted_secrets (path, team_id)
VALUES (sqlc.arg(path), sqlc.arg(team_id))
ON CONFLICT (path) DO NOTHING;

-- name: SetTeamApproval :one
-- Only the approval columns: a team that never saved its settings gets a row
-- holding the defaults for everything else, and UpsertTeamSettings, which
-- writes the rest, leaves these alone.
INSERT INTO team_settings (team_id, approval_mode, approval_ttl_minutes)
VALUES (sqlc.arg(team_id), sqlc.arg(approval_mode), sqlc.arg(approval_ttl_minutes))
ON CONFLICT (team_id) DO UPDATE SET
    approval_mode        = EXCLUDED.approval_mode,
    approval_ttl_minutes = EXCLUDED.approval_ttl_minutes,
    updated_at           = now()
RETURNING *;

-- A new team's settings: it asks before its agents write. Every other
-- column takes its default, as a team that never saved reads them.
-- name: StartTeamSettings :exec
INSERT INTO team_settings (team_id, approval_mode) VALUES ($1, 'writes');
