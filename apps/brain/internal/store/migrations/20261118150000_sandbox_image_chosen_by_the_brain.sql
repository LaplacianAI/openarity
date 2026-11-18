-- +goose Up
SET lock_timeout = '3s';

-- What a room starts from is the brain's choice, the image it was built
-- with, not a team's: a team names a provider, its address and its key.
ALTER TABLE team_settings
    DROP CONSTRAINT team_settings_sandbox_image_allowed,
    DROP COLUMN sandbox_image;

-- +goose Down
SET lock_timeout = '3s';

ALTER TABLE team_settings
    ADD COLUMN sandbox_image text,
    ADD CONSTRAINT team_settings_sandbox_image_allowed CHECK (
        sandbox_image IS NULL OR (sandbox_provider <> 'none' AND sandbox_image <> ''));
