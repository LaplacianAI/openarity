-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE teams ADD CONSTRAINT teams_name_key UNIQUE (name);

-- +goose Down
ALTER TABLE teams DROP CONSTRAINT teams_name_key;
