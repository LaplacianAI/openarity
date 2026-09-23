-- +goose Up
SET lock_timeout = '3s';

-- A tombstone can now be written before its object exists. An upload reserves
-- the keys it is about to write, so a crash anywhere leaves only objects the
-- reaper will delete — but a reservation claimed at once would be swept while
-- the upload is still running, deleting a file its row is about to name.
-- claimable_after holds a reservation back until the upload has had time to
-- commit, after which the reaper finds the row that needs it and keeps it.
--
-- Every tombstone written by a trigger is claimable at once, as before: the
-- default is now(), which also gives every existing row the migration's own
-- time. now() is stable, not volatile, so this is a metadata change and does
-- not rewrite the table.
ALTER TABLE deleted_objects
    ADD COLUMN claimable_after timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE deleted_objects DROP COLUMN claimable_after;
