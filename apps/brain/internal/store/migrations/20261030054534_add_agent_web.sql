-- +goose Up
SET lock_timeout = '3s';

-- Gives the agent web_search and web_fetch. The hub has both whatever this
-- says, so the column is about every other agent. False keeps each agent
-- that existed before it where it was.
ALTER TABLE agents ADD COLUMN web boolean NOT NULL DEFAULT false;

-- +goose Down
SET lock_timeout = '3s';

ALTER TABLE agents DROP COLUMN web;
