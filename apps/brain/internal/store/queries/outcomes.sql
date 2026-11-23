-- Top-level runs that have settled and wait to be judged: a person wrote
-- again in the session after the run finished, or ten minutes passed. Only
-- in teams with improvement on, and only runs finished since it went on.
-- name: ListSettledRuns :many
SELECT r.id, r.team_id FROM runs r
JOIN team_settings ts ON ts.team_id = r.team_id AND ts.improvement
WHERE r.parent_run_id IS NULL AND NOT r.builder
  AND r.status IN ('succeeded', 'failed', 'cancelled')
  AND ts.improvement_since IS NOT NULL AND r.finished_at > ts.improvement_since
  AND NOT EXISTS (SELECT 1 FROM run_outcomes o WHERE o.run_id = r.id)
  AND (r.finished_at < now() - make_interval(secs => sqlc.arg('settle_seconds')::int)
       OR EXISTS (SELECT 1 FROM run_events e
                  JOIN messages m ON m.session_id = r.session_id
                  WHERE e.run_id = r.id AND e.kind = 'finished'
                    AND NOT m.answer AND m.session_seq > e.session_seq))
ORDER BY r.finished_at
LIMIT sqlc.arg('page_size');

-- Judged runs waiting for their digest, in teams that still have it on.
-- name: ListPendingDigests :many
SELECT o.run_id, o.team_id, o.digest_round FROM run_outcomes o
JOIN team_settings ts ON ts.team_id = o.team_id AND ts.improvement
WHERE o.digest = 'pending'
ORDER BY o.created_at
LIMIT sqlc.arg('page_size');

-- What a check needs of the team: whether it is still on, which model
-- judges (the judge's, else the team's), and the share digested.
-- name: GetImprovementSettings :one
SELECT improvement, improvement_sample_share, improvement_daily_passes,
       improvement_pass_tokens, improvement_pass_minutes,
       improvement_efficiency, improvement_efficiency_margin,
       model_name, model_max_tokens, model_temperature,
       judge_model_name, judge_model_max_tokens, judge_model_temperature
FROM team_settings WHERE team_id = $1;

-- name: RecordRunOutcome :execrows
INSERT INTO run_outcomes (run_id, team_id, verdict, implicit, asked, missed, why, error,
                          model, input_tokens, output_tokens, sampled, digest)
VALUES (sqlc.arg(run_id), sqlc.arg(team_id), sqlc.arg(verdict), sqlc.arg(implicit), sqlc.arg(asked),
        sqlc.arg(missed), sqlc.arg(why), sqlc.narg(error), sqlc.arg(model),
        sqlc.arg(input_tokens), sqlc.arg(output_tokens), sqlc.arg(sampled), sqlc.arg(digest))
ON CONFLICT (run_id) DO NOTHING;

-- name: GetRunOutcome :one
SELECT * FROM run_outcomes WHERE run_id = $1;

-- Held for the digest, so two digests of one run cannot both write.
-- name: LockRunOutcome :one
SELECT * FROM run_outcomes WHERE run_id = $1 FOR UPDATE;

-- name: SetRunDigest :exec
UPDATE run_outcomes SET digest = sqlc.arg(digest) WHERE run_id = sqlc.arg(run_id);

-- A 👎 on a run judged good and not sampled: it is digested after all.
-- name: ReopenRunDigest :exec
UPDATE run_outcomes SET digest = 'pending', digest_round = digest_round + 1
WHERE run_id = $1 AND digest = 'skipped';

-- name: AddRunDigest :exec
INSERT INTO run_digests (run_id, agent_id, team_id, went, summary, part, target, cause, behaviour,
                         outsider, model, input_tokens, output_tokens)
VALUES (sqlc.arg(run_id), sqlc.arg(agent_id), sqlc.arg(team_id), sqlc.arg(went), sqlc.arg(summary),
        sqlc.arg(part), sqlc.arg(target), sqlc.arg(cause), sqlc.arg(behaviour), sqlc.arg(outsider),
        sqlc.arg(model), sqlc.arg(input_tokens), sqlc.arg(output_tokens));

-- name: ListRunDigests :many
SELECT * FROM run_digests WHERE run_id = $1 ORDER BY agent_id;

-- name: UpsertRunFeedback :one
INSERT INTO run_feedback (run_id, team_id, user_id, value, note, via)
VALUES (sqlc.arg(run_id), sqlc.arg(team_id), sqlc.arg(user_id), sqlc.arg(value), sqlc.arg(note), sqlc.arg(via))
ON CONFLICT (run_id, user_id) DO UPDATE SET
    value = EXCLUDED.value, note = EXCLUDED.note, via = EXCLUDED.via, updated_at = now()
RETURNING *;

-- name: DeleteRunFeedback :execrows
DELETE FROM run_feedback WHERE run_id = $1 AND user_id = $2;

-- A run's judged outcome, its feedback counts and the caller's own word,
-- in one read: every column of the outcome is null when it is not judged.
-- name: GetRunOutcomeView :one
SELECT o.verdict, o.missed, o.why, o.implicit, o.created_at AS judged_at,
       (SELECT count(*) FILTER (WHERE c.value = 'up') FROM run_feedback c WHERE c.run_id = x.id)::int AS up,
       (SELECT count(*) FILTER (WHERE c.value = 'down') FROM run_feedback c WHERE c.run_id = x.id)::int AS down,
       f.value AS my_value, f.note AS my_note, f.updated_at AS my_updated_at
FROM (SELECT sqlc.arg(run_id)::uuid AS id) x
LEFT JOIN run_outcomes o ON o.run_id = x.id
LEFT JOIN run_feedback f ON f.run_id = x.id AND f.user_id = sqlc.arg(user_id)::uuid;

-- Held while a run's feedback changes or its outcome is recorded, so a 👎
-- and the judge's verdict cannot pass each other: whichever comes second
-- sees the first. Under READ COMMITTED a plain read could not see a 👎 not
-- yet committed.
-- name: LockRunVotes :exec
SELECT pg_advisory_xact_lock(hashtext('run_votes'), hashtext(sqlc.arg(run_id)::uuid::text));

-- name: CountRunFeedback :one
SELECT count(*) FILTER (WHERE value = 'up')::int AS up,
       count(*) FILTER (WHERE value = 'down')::int AS down
FROM run_feedback WHERE run_id = $1;

-- Where the run's finish sits in its session's stream, which orders the
-- messages before and after it.
-- name: GetRunFinishedSeq :one
SELECT session_seq FROM run_events WHERE run_id = $1 AND kind = 'finished';

-- What people said in a session before a point, newest first.
-- name: ListMessagesBefore :many
SELECT sender_name, text, user_id, session_seq FROM messages
WHERE session_id = sqlc.arg(session_id) AND NOT answer AND session_seq < sqlc.arg(before)::bigint
ORDER BY session_seq DESC
LIMIT sqlc.arg(page_size);

-- What people said in a session after a point, oldest first.
-- name: ListMessagesAfter :many
SELECT sender_name, text, user_id, session_seq FROM messages
WHERE session_id = sqlc.arg(session_id) AND NOT answer AND session_seq > sqlc.arg(after)::bigint
ORDER BY session_seq
LIMIT sqlc.arg(page_size);

-- Every run of a tree, the root first, with whether its agent is one the
-- team defined (only those are digested).
-- name: ListTreeRuns :many
SELECT r.id, r.parent_run_id, r.depth, r.agent_id, r.agent_name, r.status, r.input, r.output, r.error,
       COALESCE(a.kind = 'custom', false)::bool AS custom
FROM runs r LEFT JOIN agents a ON a.id = r.agent_id
WHERE r.root_run_id = $1
ORDER BY r.depth, r.created_at, r.id;

-- name: ListTreeApprovals :many
SELECT run_id, tool, summary, status, reason FROM approvals WHERE root_run_id = $1 ORDER BY created_at, id;

-- Whether someone outside the team spoke in the session.
-- name: SessionHasOutsider :one
SELECT EXISTS (
    SELECT 1 FROM messages m
    WHERE m.session_id = sqlc.arg(session_id) AND NOT m.answer
      AND NOT EXISTS (SELECT 1 FROM team_members tm
                      WHERE tm.team_id = sqlc.arg(team_id) AND tm.user_id = m.user_id))::bool;

-- One person's feedback on every run of a session.
-- name: ListMyFeedbackInSession :many
SELECT f.run_id, f.value, f.note, f.updated_at FROM run_feedback f
JOIN runs r ON r.id = f.run_id
WHERE r.session_id = sqlc.arg(session_id) AND f.user_id = sqlc.arg(user_id)
ORDER BY f.run_id;

-- Whether one tool failed more than once anywhere in a run's tree: a run
-- that ended well may still have fought a tool, and that is worth a digest.
-- name: RunHasRepeatedToolFailures :one
SELECT EXISTS (
    SELECT 1 FROM run_events e JOIN runs r ON r.id = e.run_id
    WHERE r.root_run_id = sqlc.arg(root_run_id) AND e.kind = 'tool_result'
      AND COALESCE(e.payload->>'error', '') <> ''
    GROUP BY e.payload->>'name'
    HAVING count(*) >= sqlc.arg(at_least)::int)::bool;
