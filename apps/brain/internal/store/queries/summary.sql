-- The team at a glance, for the dashboard's home. One statement, so every
-- count is read from one snapshot and the four agree with each other: four
-- statements, even in one transaction, would each see the database as it was
-- when that statement started.
--
-- Each count obeys the rule its list does: runs and sessions by the session
-- rule, so nobody is told how many conversations they cannot open. Runs are
-- the last 24 hours on the database's clock, top-level only, as the run
-- history lists them. An aggregate with no GROUP BY returns its one row even
-- when there are no runs, so a new team reads as zeros, not as no row.
-- name: TeamSummary :one
WITH visible_runs AS (
    SELECT r.status FROM runs r
    WHERE r.team_id = sqlc.arg('team_id')
      AND r.id = r.root_run_id
      AND r.created_at >= now() - interval '24 hours'
      AND EXISTS (
        SELECT 1 FROM sessions s
        WHERE s.id = r.session_id
          AND (s.kind <> 'direct'
           OR sqlc.arg('moderator')::bool
           OR s.user_id = sqlc.arg('viewer')::uuid))
)
SELECT
    count(*) FILTER (WHERE status = 'queued')    AS runs_queued,
    count(*) FILTER (WHERE status = 'running')   AS runs_running,
    count(*) FILTER (WHERE status = 'succeeded') AS runs_succeeded,
    count(*) FILTER (WHERE status = 'failed')    AS runs_failed,
    count(*) FILTER (WHERE status = 'cancelled') AS runs_cancelled,
    (SELECT count(*) FROM sessions s
     WHERE s.team_id = sqlc.arg('team_id')
       AND (s.kind <> 'direct'
        OR sqlc.arg('moderator')::bool
        OR s.user_id = sqlc.arg('viewer')::uuid)) AS sessions,
    (SELECT count(*) FROM pending_senders p
     JOIN channels c ON c.id = p.channel_id
     WHERE c.team_id = sqlc.arg('team_id')) AS waiting_senders,
    (SELECT count(*) FROM mcp_servers m
     WHERE m.team_id = sqlc.arg('team_id')
       AND m.last_discovery_error IS NOT NULL) AS servers_failing
FROM visible_runs;
