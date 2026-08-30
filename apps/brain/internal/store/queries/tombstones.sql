-- Claim a batch to sweep, and stamp the attempt in the same statement. The
-- stamp is the lease: another sweeper ordering by last_attempt_at will not see
-- these again until the retry window passes, so two replicas need no election
-- between them.
--
-- Claiming with an UPDATE rather than SELECT ... FOR UPDATE held open: the
-- sweeper makes a network call per object, and a transaction spanning those
-- would hold row locks for as long as the object store is slow.
--
-- SKIP LOCKED covers the moment two sweepers claim at once. Without it the
-- second blocks on the first's rows and then re-reads them, which is the same
-- work done twice rather than the next batch done once.
--
-- name: ClaimDeletedObjects :many
UPDATE deleted_objects
SET attempts = attempts + 1, last_attempt_at = now()
WHERE object_key IN (
    SELECT d.object_key FROM deleted_objects d
    WHERE d.last_attempt_at IS NULL OR d.last_attempt_at < sqlc.arg('retry_before')
    ORDER BY d.last_attempt_at NULLS FIRST, d.deleted_at
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- The object is gone, so the record of work to do goes too. Called only after
-- the object store confirms, or after the count above says another row still
-- needs those bytes.
--
-- name: ForgetDeletedObject :exec
DELETE FROM deleted_objects WHERE object_key = $1;

-- What is still owed. A number that stops falling is a sweeper that stopped,
-- and it is the only external sign there is: everything else about an erasure
-- looks the same whether the bytes went or not.
--
-- One row when anything is outstanding and none when nothing is, which is the
-- honest shape: there is no oldest erasure on an empty backlog. min() would
-- have to be nullable and sqlc types a nullable aggregate as interface{};
-- count(*) OVER () carries the total on the one row this returns instead.
--
-- name: DeletedObjectBacklog :many
SELECT count(*) OVER () AS outstanding, deleted_at AS oldest
FROM deleted_objects
ORDER BY deleted_at
LIMIT 1;

-- The same three, for the secret store. Kept beside the object ones rather
-- than duplicated into a second file: they are one mechanism with two
-- destinations, and reading them together is what stops the two drifting.
--
-- name: ClaimDeletedSecrets :many
UPDATE deleted_secrets
SET attempts = attempts + 1, last_attempt_at = now()
WHERE path IN (
    SELECT d.path FROM deleted_secrets d
    WHERE d.last_attempt_at IS NULL OR d.last_attempt_at < sqlc.arg('retry_before')
    ORDER BY d.last_attempt_at NULLS FIRST, d.deleted_at
    LIMIT sqlc.arg('batch_size')
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ForgetDeletedSecret :exec
DELETE FROM deleted_secrets WHERE path = $1;

-- name: DeletedSecretBacklog :many
SELECT count(*) OVER () AS outstanding, deleted_at AS oldest
FROM deleted_secrets
ORDER BY deleted_at
LIMIT 1;
