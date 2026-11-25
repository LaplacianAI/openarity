-- +goose Up
-- Each VALIDATE reads the table under a lock that lets writes through, so the
-- keys and checks added NOT VALID are proven on the rows already there
-- without holding sessions or the replies outbox still.
SET LOCAL lock_timeout = '3s';
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_schedule_fkey;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_watcher_fkey;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_one_trigger;
ALTER TABLE replies VALIDATE CONSTRAINT replies_posted_ref_bounded;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
SELECT 1;
