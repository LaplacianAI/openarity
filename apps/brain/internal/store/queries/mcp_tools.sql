-- name: ListMCPToolsByServer :many
SELECT * FROM mcp_tools WHERE mcp_server_id = $1 ORDER BY name;

-- name: GetMCPTool :one
SELECT * FROM mcp_tools WHERE id = $1;

-- A tool whose description, schema and hints are unchanged is not rewritten, so a
-- discovery that finds nothing new leaves nothing in the outbox and costs the
-- projector nothing.
-- name: UpsertMCPTool :exec
INSERT INTO mcp_tools (mcp_server_id, team_id, name, description, input_schema, annotations)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (mcp_server_id, name) DO UPDATE SET
    description  = EXCLUDED.description,
    input_schema = EXCLUDED.input_schema,
    annotations  = EXCLUDED.annotations,
    updated_at   = now()
WHERE (mcp_tools.description, mcp_tools.input_schema, mcp_tools.annotations)
      IS DISTINCT FROM (EXCLUDED.description, EXCLUDED.input_schema, EXCLUDED.annotations);

-- An empty list deletes every tool of the server: `<> ALL('{}')` is true for
-- every row, which is right for a server that now lists nothing.
-- name: DeleteMCPToolsNotIn :exec
DELETE FROM mcp_tools
WHERE mcp_server_id = sqlc.arg('mcp_server_id')
  AND name <> ALL(sqlc.arg('names')::text[]);
