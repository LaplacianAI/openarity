-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A command server was once recorded as a discovery that failed, though the
-- brain never asks one anything. It is no longer recorded, and what was
-- recorded goes, so Home stops counting such servers as failing.
UPDATE mcp_servers SET last_discovery_error = NULL
WHERE url IS NULL AND last_discovery_error IS NOT NULL;

-- +goose Down
-- Those messages said nothing true about the server; there is nothing to put back.
