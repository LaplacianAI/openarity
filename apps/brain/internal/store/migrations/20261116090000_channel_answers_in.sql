-- +goose Up
SET LOCAL lock_timeout = '3s';

-- Where a channel answers: everywhere (direct chats, and groups where it is
-- mentioned), only groups, or only direct chats. A message outside it is
-- stored when its sender is let in and asks nothing: no run, no steer, no
-- stranger queued or told. For a number that is also a person's own.
ALTER TABLE channels
    ADD COLUMN answers_in text NOT NULL DEFAULT 'everywhere',
    ADD CONSTRAINT channels_answers_in_known CHECK (answers_in IN ('everywhere', 'groups', 'direct'));

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE channels DROP COLUMN answers_in;
