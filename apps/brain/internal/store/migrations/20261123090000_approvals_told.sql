-- +goose Up
SET LOCAL lock_timeout = '3s';

-- When the run that asked was told the answer. A request is answered by a
-- person, but acted on by the run when it resumes: an approved call is made
-- then, and every answer is said to the model once. Until it is told, a run
-- that ends its turn waits rather than finishing.
ALTER TABLE approvals ADD COLUMN told_at timestamptz;
ALTER TABLE approvals ADD CONSTRAINT approvals_told_after_decided
    CHECK (told_at IS NULL OR status <> 'pending') NOT VALID;
ALTER TABLE approvals VALIDATE CONSTRAINT approvals_told_after_decided;

-- +goose Down
SET LOCAL lock_timeout = '3s';

ALTER TABLE approvals DROP CONSTRAINT approvals_told_after_decided;
ALTER TABLE approvals DROP COLUMN told_at;
