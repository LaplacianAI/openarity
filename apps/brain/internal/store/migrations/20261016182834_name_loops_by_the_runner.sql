-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE agents DROP CONSTRAINT agents_pattern_known;

-- Which loops exist is the brain's Runner's to say, and the API checks it.
-- This only keeps a name a name.
ALTER TABLE agents ADD CONSTRAINT agents_pattern_named
    CHECK (pattern ~ '^[a-z][a-z0-9-]{0,63}$') NOT VALID;

-- +goose Down
SET lock_timeout = '3s';

ALTER TABLE agents DROP CONSTRAINT agents_pattern_named;

-- Refuses to go down while an agent uses a loop outside the six, rather
-- than leave a row the old constraint calls impossible.
ALTER TABLE agents ADD CONSTRAINT agents_pattern_known CHECK (pattern IN
    ('react', 'plan', 'rewoo', 'reflection', 'code', 'custom'));
