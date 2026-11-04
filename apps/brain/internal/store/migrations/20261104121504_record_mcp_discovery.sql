-- +goose Up
SET lock_timeout = '5s';
ALTER TABLE mcp_servers ADD COLUMN last_discovery_error text;
ALTER TABLE mcp_servers ADD COLUMN last_discovery_at timestamptz;

-- +goose Down
SET lock_timeout = '5s';
ALTER TABLE mcp_servers DROP COLUMN last_discovery_at;
ALTER TABLE mcp_servers DROP COLUMN last_discovery_error;
