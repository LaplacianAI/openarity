-- +goose Up
SET LOCAL lock_timeout = '3s';

-- An E2B team's workspace template is built at the team's size, beside the
-- default one its command servers start from. size is '' for the default and
-- '<cpus>c-<memory>m-<disk>d' otherwise, so a room waiting for its own size
-- falls back only to an older release's template of the same size. The
-- CHECK is validated at once: every row there is takes the default.
ALTER TABLE e2b_templates
    ADD COLUMN size text NOT NULL DEFAULT '',
    ADD CONSTRAINT e2b_templates_size_shape CHECK (size = '' OR size ~ '^[1-9][0-9]*c-[1-9][0-9]*m-[1-9][0-9]*d$');

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE e2b_templates DROP COLUMN size;
