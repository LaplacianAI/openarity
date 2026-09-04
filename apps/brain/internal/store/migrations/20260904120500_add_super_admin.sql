-- +goose Up

-- Super admin has been a startup-time set built from OPENARITY_SUPER_ADMINS,
-- which cannot answer the question a fresh install asks: who may create the
-- first team, before anybody has logged in and before there is a `sub` to put
-- in that variable? The env var stays — it is how a deployment pins the answer
-- in advance — and this column is the other source, the one that can be
-- written while the brain is running.
--
-- The two are OR'd rather than merged. A deployment that lists subjects keeps
-- working with no migration of its values into rows, and a row granted here
-- cannot be revoked by editing the environment, which is the property that
-- makes the promotion durable.
ALTER TABLE users ADD COLUMN is_super_admin boolean NOT NULL DEFAULT false;

-- The only question ever asked of this column is "does any super admin exist",
-- on a path that runs for every authenticated request. Partial, because the
-- rows that are false are the entire table on a normal deployment and indexing
-- them buys nothing.
CREATE INDEX users_is_super_admin ON users (is_super_admin) WHERE is_super_admin;

-- +goose Down
DROP INDEX users_is_super_admin;
ALTER TABLE users DROP COLUMN is_super_admin;
