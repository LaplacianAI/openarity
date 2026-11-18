-- +goose Up
SET lock_timeout = '3s';

-- What a room starts from is the brain's choice, the image it was built
-- with, not a team's. E2B alone cannot be handed an image: it starts rooms
-- from a template built in the team's own E2B account, so an E2B team may
-- name that template, and the brain's default name is used when it does not.
-- An image any other provider was given is dropped.
UPDATE team_settings SET sandbox_image = NULL
WHERE sandbox_image IS NOT NULL AND sandbox_provider <> 'e2b';

ALTER TABLE team_settings DROP CONSTRAINT team_settings_sandbox_image_allowed;
ALTER TABLE team_settings RENAME COLUMN sandbox_image TO sandbox_template;
ALTER TABLE team_settings ADD CONSTRAINT team_settings_sandbox_template_e2b_only CHECK (
    sandbox_template IS NULL OR (sandbox_provider = 'e2b' AND sandbox_template <> ''));

-- +goose Down
SET lock_timeout = '3s';

ALTER TABLE team_settings DROP CONSTRAINT team_settings_sandbox_template_e2b_only;
ALTER TABLE team_settings RENAME COLUMN sandbox_template TO sandbox_image;
ALTER TABLE team_settings ADD CONSTRAINT team_settings_sandbox_image_allowed CHECK (
    sandbox_image IS NULL OR (sandbox_provider <> 'none' AND sandbox_image <> ''));
