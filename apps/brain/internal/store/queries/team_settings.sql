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
    learning, compaction_messages, hub_agent_id
) VALUES (
    sqlc.arg(team_id),
    sqlc.narg(model_name), sqlc.narg(model_max_tokens), sqlc.narg(model_temperature),
    sqlc.arg(search_provider), sqlc.narg(search_url), sqlc.narg(search_secret_id),
    sqlc.arg(learning), sqlc.arg(compaction_messages), sqlc.narg(hub_agent_id)
)
ON CONFLICT (team_id) DO UPDATE SET
    model_name          = EXCLUDED.model_name,
    model_max_tokens    = EXCLUDED.model_max_tokens,
    model_temperature   = EXCLUDED.model_temperature,
    search_provider     = EXCLUDED.search_provider,
    search_url          = EXCLUDED.search_url,
    search_secret_id    = EXCLUDED.search_secret_id,
    learning            = EXCLUDED.learning,
    compaction_messages = EXCLUDED.compaction_messages,
    hub_agent_id        = EXCLUDED.hub_agent_id,
    updated_at          = now()
RETURNING *;

-- name: RecordDeletedSecret :exec
-- A key that reached the secret store and then lost its save: recorded so the
-- sweep removes it, rather than a credential nothing names.
INSERT INTO deleted_secrets (path, team_id)
VALUES (sqlc.arg(path), sqlc.arg(team_id))
ON CONFLICT (path) DO NOTHING;
