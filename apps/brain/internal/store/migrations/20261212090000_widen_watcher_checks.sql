-- +goose Up
-- A watcher's check ends as a schedule's fire does: a run cancelled through
-- the API is cancelled, not failed, and an agent deleted is skipped_target.
-- The old check goes and the new one is proven on the rows in the next
-- migration, so nothing waits on a scan here.
SET LOCAL lock_timeout = '3s';
ALTER TABLE watcher_checks DROP CONSTRAINT watcher_checks_outcome_known;
ALTER TABLE watcher_checks ADD CONSTRAINT watcher_checks_outcome_known CHECK (outcome IN (
    'unchanged', 'baseline', 'changed', 'held', 'error', 'delivered', 'duplicate',
    'skipped_cap', 'skipped_maker', 'skipped_target', 'silent', 'answered', 'failed', 'cancelled')) NOT VALID;

-- A deleted poller lets go of its target, so the sweep can delete a target
-- nobody watches; a live one always has one.
ALTER TABLE watchers DROP CONSTRAINT watchers_source_shape;
ALTER TABLE watchers ADD CONSTRAINT watchers_source_shape CHECK (
    (source = 'webhook') = (hook_id IS NOT NULL) AND
    (source <> 'webhook' OR target_id IS NULL) AND
    (source = 'webhook' OR status = 'deleted' OR target_id IS NOT NULL) AND
    (source = 'webhook') = (every_seconds IS NULL) AND
    (selector IS NULL OR source = 'web_page')) NOT VALID;

-- +goose Down
-- Fails, and must, once a check was recorded as one of the new outcomes, or
-- a deleted poller let go of its target: those rows are what the old checks
-- forbid. Delete or relabel them first.
SET LOCAL lock_timeout = '3s';
ALTER TABLE watchers DROP CONSTRAINT watchers_source_shape;
ALTER TABLE watchers ADD CONSTRAINT watchers_source_shape CHECK (
    (source = 'webhook') = (hook_id IS NOT NULL) AND
    (source = 'webhook') = (target_id IS NULL) AND
    (source = 'webhook') = (every_seconds IS NULL) AND
    (selector IS NULL OR source = 'web_page'));
ALTER TABLE watcher_checks DROP CONSTRAINT watcher_checks_outcome_known;
ALTER TABLE watcher_checks ADD CONSTRAINT watcher_checks_outcome_known CHECK (outcome IN (
    'unchanged', 'baseline', 'changed', 'held', 'error', 'delivered', 'duplicate',
    'skipped_cap', 'skipped_maker', 'silent', 'answered', 'failed'));
