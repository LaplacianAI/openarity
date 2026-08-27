---
name: write-migration
description: Write a database migration for the brain — a new table, a new column, an index, a type change, a backfill, or a drop. Covers the goose file format, lock safety, expand-contract for column changes, batched backfills, and when an index needs CONCURRENTLY. Use for every schema change.
---

# Write a migration

Migrations live in `internal/store/migrations`, are embedded into the binary,
and are applied by `brain migrate up` — never by the `goose` CLI.

The whole point of this skill is that a migration which is instant on an empty
table can freeze a busy one. Habits set now are free; retrofitting them later
is not.

## Step 1 — scaffold the file

```sh
cd apps/brain && make migration name=add_agents_archived_at
```

Timestamped, never sequential. Two people writing a migration on the same day
would both pick `00007` and produce a conflict git cannot resolve.

Name it after what it does, not what it touches: `add_agents_archived_at`, not
`update_agents`.

## Step 2 — always start with a lock timeout

```sql
-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE agents ADD COLUMN archived_at timestamptz;
```

**This line goes in every migration that touches an existing table.**

Postgres queues lock requests fairly. A migration waiting for `ACCESS
EXCLUSIVE` blocks every query that arrives after it — including plain reads
that would otherwise have run. So one slow `SELECT` already in flight turns a
one-millisecond `ALTER` into a table-wide outage lasting as long as that query.

With `lock_timeout` the migration fails fast, the Job retries, and nobody
queues behind it. Failing is cheap. Blocking is not.

## Step 0 — is the migration you want to change already merged?

**A merged migration is a migration that has run.** `brain migrate up` records
each file by name and never looks at it again, so editing one that has been
applied leaves the file and the deployed schema disagreeing, silently and
permanently. Nothing re-runs it, and nothing reports the difference.

The test is `git log origin/main -- <the file>`. If it is there, the change is
a **new** migration that alters what the old one created, whatever the old one
said and however recently it merged.

This nearly went wrong on `attachments`: the plan was to fold a column into
the migration that created the table, and that migration had merged forty
minutes earlier while the work was on another branch.

## Step 3 — write the Down

```sql
-- +goose Down
ALTER TABLE agents DROP COLUMN archived_at;
```

Not because production rollbacks are common — they are not. Because writing it
forces you to notice when a change *cannot* be reversed, which is exactly when
it needs a different plan. A `Down` you cannot write is a design review, not a
formality.

**Reverse what you replaced, not only what you added.** A migration that swaps
one constraint for another has to put the original back:

```sql
-- +goose Up
ALTER TABLE attachments DROP CONSTRAINT attachments_message_id_fkey;
ALTER TABLE attachments ADD CONSTRAINT attachments_message_in_session
    FOREIGN KEY (message_id, session_id) REFERENCES messages (id, session_id)
    ON DELETE CASCADE;

-- +goose Down
ALTER TABLE attachments DROP CONSTRAINT attachments_message_in_session;
ALTER TABLE attachments ADD CONSTRAINT attachments_message_id_fkey
    FOREIGN KEY (message_id) REFERENCES messages (id) ON DELETE CASCADE;
```

Dropping the new one alone leaves the table with *no* reference where it had
one — a rollback that loses an invariant rather than restoring it. Assert both
directions by name:

```sh
psql -tAc "SELECT count(*) FROM pg_constraint WHERE conname='attachments_message_id_fkey'"
```

## Step 4 — check the operation against this table

| Operation | Cost | Safe as-is? |
|---|---|---|
| `CREATE TABLE` | none — nothing references it | yes |
| `ADD COLUMN` with a constant default | metadata only | yes |
| `ADD COLUMN` with a volatile default (`now()`, `gen_random_uuid()`) | full table rewrite | no — add nullable, then backfill |
| `DROP COLUMN` | metadata only | yes |
| `CREATE INDEX` | blocks **writes** for the whole build | no on a large table — see step 6 |
| `SET NOT NULL` | full scan under `ACCESS EXCLUSIVE` | no — see step 5 |
| `ADD FOREIGN KEY` | locks **both** tables while validating | no — `NOT VALID`, then `VALIDATE` |
| change a column's type | full rewrite | no — expand-contract |
| `RENAME COLUMN` | instant, but breaks running code | no — expand-contract |

Reads are never blocked by any of these. Writes are what stops.

## Step 5 — expand-contract for anything that changes an existing column

Never change a column in one step. Four small migrations, each safe on its own,
with a deploy between them:

```
1. add the new column, nullable          migration
2. deploy code that writes BOTH columns  deploy
3. backfill the new column in batches    migration (step 7)
4. deploy code that reads the new one    deploy
5. drop the old column                   migration
```

The rule that makes it work: **at every point, the running code and the schema
must both be valid.** A rename breaks that — old pods query a column that no
longer exists the instant the migration lands.

Same shape for `SET NOT NULL`, which otherwise scans the whole table under an
exclusive lock:

```sql
-- +goose Up
SET lock_timeout = '3s';

ALTER TABLE agents
    ADD CONSTRAINT agents_name_not_null CHECK (name IS NOT NULL) NOT VALID;
```

Then, in a **separate** migration:

```sql
-- +goose Up
ALTER TABLE agents VALIDATE CONSTRAINT agents_name_not_null;
```

`NOT VALID` skips the scan. `VALIDATE` scans without an exclusive lock.

Foreign keys work identically — `ADD CONSTRAINT ... NOT VALID`, then
`VALIDATE CONSTRAINT` separately.

## Step 5b — denormalise only when no index can express the access path

A second copy of a fact is normally a trade: faster reads against the risk that
the copies drift. Postgres can take the second half away, which changes the
decision.

A unique key on the source lets the copy be part of a **composite foreign
key**, so a row that disagrees cannot be written:

```sql
ALTER TABLE messages ADD CONSTRAINT messages_id_session_key UNIQUE (id, session_id);

ALTER TABLE attachments
    ADD CONSTRAINT attachments_message_in_session
        FOREIGN KEY (message_id, session_id) REFERENCES messages (id, session_id);
```

What is left is the bytes and the index. So the question is only whether the
access path is real — and that is measurable, not a guess:

```text
SELECT a.* FROM attachments a JOIN messages m ON m.id = a.message_id
WHERE m.session_id = $1;                        13.6 ms warm, 591 buffers

SELECT * FROM attachments WHERE session_id = $1; 0.36 ms warm,  25 buffers
```

Seed a realistic corpus and `EXPLAIN (ANALYZE, BUFFERS)` both. Toy data proves
nothing: with five attachments the planner inverted the join and answered in
0.1 ms, which said only that five rows are cheap.

The distinction worth keeping: denormalising because you expect load is a bet.
Denormalising because **the schema cannot express the question** — no index
covers "attachments in this session" through the join, so both sides are read
in full — is a structural fact.

## Step 6 — indexes on a table with rows

```sql
-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_tenant
    ON agents (tenant_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_agents_tenant;
```

Three things, all required together:

- **`CONCURRENTLY`** builds without blocking writers. It costs two table scans
  instead of one.
- **`-- +goose NO TRANSACTION`** because `CONCURRENTLY` cannot run inside a
  transaction. goose wraps migrations in one by default and Postgres rejects it.
- **`IF NOT EXISTS`** because a failed `CONCURRENTLY` build leaves an **INVALID**
  index behind. It is not rolled back — there is no transaction to roll back.
  The retry then collides with the corpse.

A migration with `NO TRANSACTION` is not atomic. If it fails halfway you clean
up by hand: `DROP INDEX CONCURRENTLY IF EXISTS ...`, then re-run.

On an empty table, plain `CREATE INDEX` inside the table's own `CREATE TABLE`
migration is fine. The care starts once there are rows.

## Step 7 — backfills go in batches, never one statement

```sql
-- +goose NO TRANSACTION
-- +goose Up
DO $$
DECLARE
    updated integer;
BEGIN
    LOOP
        UPDATE agents SET archived_at = created_at
        WHERE archived_at IS NULL AND id IN (
            SELECT id FROM agents WHERE archived_at IS NULL LIMIT 1000
        );
        GET DIAGNOSTICS updated = ROW_COUNT;
        EXIT WHEN updated = 0;
        COMMIT;
    END LOOP;
END $$;
```

One `UPDATE` over 50M rows is a single transaction holding 50M row locks,
generating enormous WAL, and blocking `VACUUM` for its whole duration. If it
fails at 90% you get none of it.

Batches commit as they go, are interruptible, and are resumable — the
`WHERE archived_at IS NULL` makes a re-run pick up where it stopped.

Large backfills are often better run as a one-off task rather than in the
deploy path at all. A migration that takes twenty minutes holds up the rollout.

## Step 8 — apply it

```sh
cd apps/brain && go run ./cmd/brain migrate up
```

**Never `goose up` against a real database.** The CLI reads files from disk;
the binary carries an embedded copy. Applying with the CLI means running a
different set from what the deployed binary believes it has, and the two drift
silently. The CLI is for scaffolding files, nothing else.

In Kubernetes this is a Job that runs to completion before the Deployment rolls
— never an `initContainer` on every pod, and never inside `run()`.

## Step 9 — tests

Migration tests need a real database, so they read `BRAIN_TEST_POSTGRES_DSN`
and skip when it is unset. CI provides it from a service container.

They cannot be `t.Parallel()` with each other — they share one schema.

Assert at least:

1. **Up applies cleanly** from an empty database.
2. **Down reverses it** — run Up, Down, Up again. A `Down` that does not
   actually reverse fails on the second Up, and only that sequence catches it.
3. **Up is idempotent** — running it twice applies zero the second time.

## Quick checklist

- [ ] `SET lock_timeout = '3s'` if the table already exists
- [ ] the migration being changed is not already merged — if it is, write a new one
- [ ] `Down` written, and it genuinely reverses, including anything Up *replaced*
- [ ] no volatile default on `ADD COLUMN`
- [ ] index on a populated table uses `CONCURRENTLY` + `NO TRANSACTION` + `IF NOT EXISTS`
- [ ] column change split into expand-contract steps
- [ ] backfill batched, not one statement
- [ ] `NOT VALID` + separate `VALIDATE` for constraints and foreign keys
- [ ] `make check` passes
