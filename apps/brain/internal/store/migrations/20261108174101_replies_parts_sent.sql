-- A long reply goes out in parts; a retry resumes at the first part not sent.
-- A constant default is stored in the catalogue, so this rewrites no rows and
-- holds its lock only for the catalogue update.

-- +goose Up
ALTER TABLE replies ADD COLUMN parts_sent integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE replies DROP COLUMN parts_sent;
