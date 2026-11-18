-- +goose Up
SET LOCAL lock_timeout = '3s';

-- A channel user is a sender approved as someone new: a user of their own,
-- with issuer openarity:channel, whom no token ever resolves to. It has no
-- email, so the name the approver gave, or the sender's own, is what
-- Members and the run history show. Null for a user who logs in.
ALTER TABLE users
    ADD COLUMN name text,
    ADD CONSTRAINT users_name_bounded CHECK (char_length(name) BETWEEN 1 AND 64);

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE users DROP COLUMN name;
