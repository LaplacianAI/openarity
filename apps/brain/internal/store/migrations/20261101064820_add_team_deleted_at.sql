-- +goose Up
SET lock_timeout = '5s';
ALTER TABLE teams ADD COLUMN deleted_at timestamptz;
CREATE UNIQUE INDEX teams_live_name_key ON teams (name) WHERE deleted_at IS NULL;
CREATE INDEX teams_deleted_at_idx ON teams (deleted_at) WHERE deleted_at IS NOT NULL;
ALTER TABLE teams DROP CONSTRAINT teams_name_key;

-- +goose Down
SET lock_timeout = '5s';

-- Fails with 23505 if a deleted team and a live team share a name, which the
-- Up made possible. Purge or rename one of them first: this runs before
-- anything is dropped, so a failure leaves the Up's schema whole.
ALTER TABLE teams ADD CONSTRAINT teams_name_key UNIQUE (name);
DROP INDEX teams_deleted_at_idx;
DROP INDEX teams_live_name_key;
ALTER TABLE teams DROP COLUMN deleted_at;
