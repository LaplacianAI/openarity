-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A rule's demand that the person a call is for may not approve it, copied to
-- the request when it is made: deciding reads the request, not today's rules,
-- so a rule changed while a call waits does not change who may answer it.
ALTER TABLE approvals ADD COLUMN require_other boolean NOT NULL DEFAULT false;

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE approvals DROP COLUMN require_other;
