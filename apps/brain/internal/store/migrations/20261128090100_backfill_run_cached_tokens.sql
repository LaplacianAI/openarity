-- +goose NO TRANSACTION
-- +goose Up
-- The runs from before cached_input_tokens get it from their usage events,
-- which have always carried it, held to the run's input. A thousand runs a
-- transaction, in id order, so each batch holds only its own row locks and
-- commits. It only ever raises a run's count: a run still going during the
-- deploy may have added to it since the batch read its events, and that is
-- kept. A re-run changes nothing.
-- +goose StatementBegin
DO $$
DECLARE
    last uuid := '00000000-0000-0000-0000-000000000000';
    upto uuid;
BEGIN
    LOOP
        -- The thousandth id past the last batch, or the last one there is.
        SELECT id INTO upto FROM (
            SELECT id FROM runs WHERE id > last ORDER BY id LIMIT 1000
        ) batch ORDER BY id DESC LIMIT 1;
        EXIT WHEN upto IS NULL;

        UPDATE runs r SET cached_input_tokens = greatest(r.cached_input_tokens, least(u.cached, r.input_tokens))
        FROM (
            SELECT run_id, sum((payload->>'cached_input_tokens')::bigint) AS cached
            FROM run_events
            WHERE kind = 'usage' AND payload ? 'cached_input_tokens'
              AND run_id > last AND run_id <= upto
            GROUP BY run_id
        ) u
        WHERE u.run_id = r.id AND u.cached > 0
          AND r.cached_input_tokens < least(u.cached, r.input_tokens);

        last := upto;
        COMMIT;
    END LOOP;
END $$;
-- +goose StatementEnd

-- Scans without the exclusive lock: writes carry on meanwhile.
ALTER TABLE runs VALIDATE CONSTRAINT runs_cached_within_input;

-- +goose Down
-- The column, and what it holds, go with the migration before.
SELECT 1;
