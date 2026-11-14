-- +goose Up
SET lock_timeout = '3s';

-- A channel says what it is sent while a run works: its answer alone, or each
-- step a person would follow as a message of its own, then the answer.
ALTER TABLE channels
    ADD COLUMN reply_mode text NOT NULL DEFAULT 'final',
    ADD CONSTRAINT channels_reply_mode_known CHECK (reply_mode IN ('final', 'events'));

-- An event is a reply of its own, sent through the outbox like the answer so
-- a conversation's events go out in order and before it. event_seq is the
-- run event it came from: a run has one answer and any number of events, and
-- an event written twice is one row.
ALTER TABLE replies
    ADD COLUMN event_seq integer,
    DROP CONSTRAINT replies_one_per_run,
    DROP CONSTRAINT replies_kind_known,
    ADD CONSTRAINT replies_kind_known CHECK (kind IN ('answer', 'apology', 'event')),
    ADD CONSTRAINT replies_event_has_seq CHECK ((kind = 'event') = (event_seq IS NOT NULL));

CREATE UNIQUE INDEX replies_one_per_run ON replies (run_id) WHERE event_seq IS NULL;
CREATE UNIQUE INDEX replies_one_per_event ON replies (run_id, event_seq) WHERE event_seq IS NOT NULL;

-- +goose Down
SET lock_timeout = '3s';

-- Events were never more than a view of a run's steps; the answers stay.
DELETE FROM replies WHERE kind = 'event';

DROP INDEX replies_one_per_event;
DROP INDEX replies_one_per_run;

ALTER TABLE replies
    DROP CONSTRAINT replies_event_has_seq,
    DROP CONSTRAINT replies_kind_known,
    ADD CONSTRAINT replies_kind_known CHECK (kind IN ('answer', 'apology')),
    ADD CONSTRAINT replies_one_per_run UNIQUE (run_id),
    DROP COLUMN event_seq;

ALTER TABLE channels
    DROP CONSTRAINT channels_reply_mode_known,
    DROP COLUMN reply_mode;
