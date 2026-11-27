-- +goose Up
-- An agent's change to a schedule or watcher that already exists waits for
-- its maker's yes beside it: the row keeps running as it was, and pending
-- holds the change, applied when a later message from the maker confirms it.
-- pending_run proposed it, in pending_in, at pending_at; unconfirmed within
-- the hour, the sweep drops it. Every row starts with none, so each check
-- holds on what is there.
SET LOCAL lock_timeout = '3s';
ALTER TABLE schedules
    ADD COLUMN pending     jsonb,
    ADD COLUMN pending_run uuid,
    ADD COLUMN pending_in  uuid REFERENCES sessions ON DELETE SET NULL,
    ADD COLUMN pending_at  timestamptz,
    ADD CONSTRAINT schedules_pending_says_when CHECK ((pending IS NULL) = (pending_at IS NULL));
ALTER TABLE watchers
    ADD COLUMN pending     jsonb,
    ADD COLUMN pending_run uuid,
    ADD COLUMN pending_in  uuid REFERENCES sessions ON DELETE SET NULL,
    ADD COLUMN pending_at  timestamptz,
    ADD CONSTRAINT watchers_pending_says_when CHECK ((pending IS NULL) = (pending_at IS NULL));

-- +goose Down
SET LOCAL lock_timeout = '3s';
ALTER TABLE watchers DROP COLUMN pending, DROP COLUMN pending_run, DROP COLUMN pending_in, DROP COLUMN pending_at;
ALTER TABLE schedules DROP COLUMN pending, DROP COLUMN pending_run, DROP COLUMN pending_in, DROP COLUMN pending_at;
