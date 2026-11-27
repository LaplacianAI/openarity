-- +goose Up
-- An agent's change to a schedule or watcher that already exists waits for
-- its maker's yes beside it: the row keeps running as it was, and pending
-- holds the change (a new name, objective or time, or a delete, a resume or
-- a run now), applied when a later message from the maker confirms it.
-- pending_run proposed it, in pending_in, at pending_at; unconfirmed within
-- the hour, the sweep drops it. Every row starts with none, so each check
-- holds on what is there.
--
-- proposed_at is when a proposal was last made or revised: a message is a
-- yes to it only if sent after, and it lapses an hour from then. Until now a
-- proposal could not be revised, so it is its created_at.
--
-- yes_code is the code a tool gave the model for the person to send back
-- with their yes, to whatever waits on the row, a proposal or an ask. Text a
-- run read cannot make the person type it, so only their own reply confirms
-- (*user*, 2026-11-28). A row waits on one at a time, and an ask always has
-- one; a proposal made before codes has none, and lapses within the hour.
SET LOCAL lock_timeout = '3s';
ALTER TABLE schedules
    ADD COLUMN proposed_at timestamptz,
    ADD COLUMN pending     jsonb,
    ADD COLUMN pending_run uuid,
    ADD COLUMN pending_in  uuid REFERENCES sessions ON DELETE SET NULL,
    ADD COLUMN pending_at  timestamptz,
    ADD CONSTRAINT schedules_pending_says_when CHECK ((pending IS NULL) = (pending_at IS NULL)),
    ADD COLUMN yes_code    text,
    ADD CONSTRAINT schedules_pending_says_who CHECK ((pending IS NULL) = (pending_run IS NULL)),
    ADD CONSTRAINT schedules_pending_has_code CHECK (pending IS NULL OR yes_code IS NOT NULL);
ALTER TABLE watchers
    ADD COLUMN proposed_at timestamptz,
    ADD COLUMN pending     jsonb,
    ADD COLUMN pending_run uuid,
    ADD COLUMN pending_in  uuid REFERENCES sessions ON DELETE SET NULL,
    ADD COLUMN pending_at  timestamptz,
    ADD CONSTRAINT watchers_pending_says_when CHECK ((pending IS NULL) = (pending_at IS NULL)),
    ADD COLUMN yes_code    text,
    ADD CONSTRAINT watchers_pending_says_who CHECK ((pending IS NULL) = (pending_run IS NULL)),
    ADD CONSTRAINT watchers_pending_has_code CHECK (pending IS NULL OR yes_code IS NOT NULL);
UPDATE schedules SET proposed_at = created_at WHERE status = 'proposed';
UPDATE watchers SET proposed_at = created_at WHERE status IN ('proposed', 'waiting_secret');
-- A proposal lapses by proposed_at, so one without it would never lapse.
ALTER TABLE schedules ADD CONSTRAINT schedules_proposed_says_when CHECK (status <> 'proposed' OR proposed_at IS NOT NULL);
ALTER TABLE watchers ADD CONSTRAINT watchers_proposed_says_when CHECK (status <> 'proposed' OR proposed_at IS NOT NULL);

-- What the sweep lapses, read through the times it lapses by.
DROP INDEX schedules_proposed;
CREATE INDEX schedules_proposed ON schedules (proposed_at) WHERE status = 'proposed';
CREATE INDEX watchers_proposed_at ON watchers (proposed_at) WHERE status = 'proposed';
CREATE INDEX schedules_pending ON schedules (pending_at) WHERE pending IS NOT NULL;
CREATE INDEX watchers_pending ON watchers (pending_at) WHERE pending IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '3s';
DROP INDEX watchers_pending;
DROP INDEX schedules_pending;
DROP INDEX watchers_proposed_at;
DROP INDEX schedules_proposed;
CREATE INDEX schedules_proposed ON schedules (created_at) WHERE status = 'proposed';
ALTER TABLE watchers DROP COLUMN proposed_at, DROP COLUMN pending, DROP COLUMN pending_run, DROP COLUMN pending_in, DROP COLUMN pending_at, DROP COLUMN yes_code;
ALTER TABLE schedules DROP COLUMN proposed_at, DROP COLUMN pending, DROP COLUMN pending_run, DROP COLUMN pending_in, DROP COLUMN pending_at, DROP COLUMN yes_code;
