-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A run that waits for a person asks them in the conversation: an approval
-- reply, keyed like an event by the run event it came from (the waiting
-- event), so a run that asks again later asks again. A notice is the brain's
-- short word on an answer given in the chat (approved, which code, only Sam
-- can answer), one at most for each message it answers. detail is what a
-- platform needs beyond the text: the requests a prompt's buttons answer, and
-- the handle a tap came with, to answer the tap where it was made.
ALTER TABLE replies
    ADD COLUMN detail jsonb,
    DROP CONSTRAINT replies_kind_known,
    DROP CONSTRAINT replies_event_has_seq,
    ADD CONSTRAINT replies_kind_known CHECK (kind IN ('answer', 'apology', 'event', 'approval', 'notice')),
    ADD CONSTRAINT replies_event_has_seq CHECK ((kind IN ('event', 'approval')) = (event_seq IS NOT NULL)),
    ADD CONSTRAINT replies_notice_answers CHECK (kind <> 'notice' OR in_reply_to IS NOT NULL);

DROP INDEX replies_one_per_run;
CREATE UNIQUE INDEX replies_one_per_run ON replies (run_id) WHERE kind IN ('answer', 'apology');
CREATE UNIQUE INDEX replies_one_notice_per_message ON replies (in_reply_to) WHERE kind = 'notice';

-- +goose Down
SET LOCAL lock_timeout = '3s';

-- What a person was asked and told in the chat was sent or is moot; the
-- answers and events stay.
DELETE FROM replies WHERE kind IN ('approval', 'notice');

DROP INDEX replies_one_notice_per_message;
DROP INDEX replies_one_per_run;
CREATE UNIQUE INDEX replies_one_per_run ON replies (run_id) WHERE event_seq IS NULL;

ALTER TABLE replies
    DROP CONSTRAINT replies_notice_answers,
    DROP CONSTRAINT replies_event_has_seq,
    DROP CONSTRAINT replies_kind_known,
    ADD CONSTRAINT replies_kind_known CHECK (kind IN ('answer', 'apology', 'event')),
    ADD CONSTRAINT replies_event_has_seq CHECK ((kind = 'event') = (event_seq IS NOT NULL)),
    DROP COLUMN detail;
