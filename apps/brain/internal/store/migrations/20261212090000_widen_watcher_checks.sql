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
-- The old checks come back NOT VALID, so rolling back neither scans nor
-- fails on rows written since (a check recorded as one of the new outcomes, a
-- deleted poller with no target); new writes are held to them at once.
-- Relabel those rows before validating them again.
SET LOCAL lock_timeout = '3s';
ALTER TABLE watchers DROP CONSTRAINT watchers_source_shape;
ALTER TABLE watchers ADD CONSTRAINT watchers_source_shape CHECK (
    (source = 'webhook') = (hook_id IS NOT NULL) AND
    (source = 'webhook') = (target_id IS NULL) AND
    (source = 'webhook') = (every_seconds IS NULL) AND
    (selector IS NULL OR source = 'web_page')) NOT VALID;
ALTER TABLE watcher_checks DROP CONSTRAINT watcher_checks_outcome_known;
ALTER TABLE watcher_checks ADD CONSTRAINT watcher_checks_outcome_known CHECK (outcome IN (
    'unchanged', 'baseline', 'changed', 'held', 'error', 'delivered', 'duplicate',
    'skipped_cap', 'skipped_maker', 'silent', 'answered', 'failed')) NOT VALID;
