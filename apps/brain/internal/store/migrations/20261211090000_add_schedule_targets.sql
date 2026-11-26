-- +goose Up
-- Where a schedule or a watcher answers: its own conversation on the
-- dashboard, a chat its maker talked in, a channel's top level, or its
-- maker's direct messages. The session holds where it posts; this says which
-- of these it was asked for, for showing it and for the rules that differ
-- (a channel's top level needs channel:write).
SET LOCAL lock_timeout = '3s';

-- target_label is how it was shown when it was picked ("#releases", "Slack:
-- a thread"): a channel's name is the platform's, read once then, so it is
-- kept rather than asked again.
ALTER TABLE schedules
    ADD COLUMN target text NOT NULL DEFAULT 'own',
    ADD COLUMN target_label text NOT NULL DEFAULT '';
ALTER TABLE schedules ADD CONSTRAINT schedules_target_known
    CHECK (target IN ('own', 'chat', 'channel', 'direct')) NOT VALID;
ALTER TABLE schedules ADD CONSTRAINT schedules_target_label_bounded
    CHECK (length(target_label) <= 200) NOT VALID;

ALTER TABLE watchers
    ADD COLUMN target text NOT NULL DEFAULT 'own',
    ADD COLUMN target_label text NOT NULL DEFAULT '';
ALTER TABLE watchers ADD CONSTRAINT watchers_target_known
    CHECK (target IN ('own', 'chat', 'channel', 'direct')) NOT VALID;
ALTER TABLE watchers ADD CONSTRAINT watchers_target_label_bounded
    CHECK (length(target_label) <= 200) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '3s';
ALTER TABLE watchers DROP COLUMN target, DROP COLUMN target_label;
ALTER TABLE schedules DROP COLUMN target, DROP COLUMN target_label;
