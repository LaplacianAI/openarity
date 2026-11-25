-- +goose Up
-- Each VALIDATE reads the table under a lock that lets writes through, so the
-- checks added NOT VALID are proven on the rows already there without holding
-- the replies outbox still. replies_kind_known stays NOT VALID, as every
-- replacement of it has.
SET LOCAL lock_timeout = '3s';
ALTER TABLE replies VALIDATE CONSTRAINT replies_file_in_session;
ALTER TABLE replies VALIDATE CONSTRAINT replies_file_names_its_file;
ALTER TABLE replies VALIDATE CONSTRAINT replies_call_id_bounded;

-- +goose Down
-- Validating leaves nothing to undo; the constraints go with the migration before.
SELECT 1;
