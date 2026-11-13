-- +goose Up

-- A top-level run continues its session's transcript: every run in a
-- conversation reads what the ones before it did. transcript is the SDK
-- session key the run's messages and steers are kept under, set when the run is
-- created so a steer to a queued run finds it: 'session:<session id>' for a
-- top-level run. NULL is the run's own id: a delegate's, which is its own,
-- or a run's from before this.
--
-- transcript_from is where the run's own first message sits in that
-- transcript, written before the message is: a run that has it is resuming,
-- and one whose transcript is no longer than it lost its first message and
-- writes it again.
ALTER TABLE runs
    ADD COLUMN transcript text,
    ADD COLUMN transcript_from int CHECK (transcript_from >= 0);

-- A transcript has no foreign key: the SDK keys it by a string. A run deleted
-- takes its own transcript with it, and the last run of a session takes the
-- session's, so a team's cascade leaves no conversation behind.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION runs_forget_transcripts() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM agent_messages WHERE session IN (
        SELECT g.id::text FROM gone g
        UNION
        SELECT g.transcript FROM gone g
        WHERE g.transcript IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.transcript = g.transcript));
    DELETE FROM agent_steers WHERE session IN (
        SELECT g.id::text FROM gone g
        UNION
        SELECT g.transcript FROM gone g
        WHERE g.transcript IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.transcript = g.transcript));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE INDEX runs_transcript ON runs (transcript) WHERE transcript IS NOT NULL;

-- +goose Down

-- Shared transcripts go first: once the column is gone nothing can name them,
-- and a run from before reads its own id, which these were never under.
DELETE FROM agent_messages WHERE session IN (SELECT DISTINCT transcript FROM runs WHERE transcript LIKE 'session:%');
DELETE FROM agent_steers   WHERE session IN (SELECT DISTINCT transcript FROM runs WHERE transcript LIKE 'session:%');

DROP INDEX runs_transcript;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION runs_forget_transcripts() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM agent_messages WHERE session IN (SELECT id::text FROM gone);
    DELETE FROM agent_steers   WHERE session IN (SELECT id::text FROM gone);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

ALTER TABLE runs DROP COLUMN transcript_from, DROP COLUMN transcript;
