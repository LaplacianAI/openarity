---
name: write-query
description: Add or change a SQL query for the brain — anything that reads or writes Postgres. Covers the sqlc annotations, regenerating and committing the output, type overrides, when a write needs Store.InTx, and the batch and copy modes for bulk work. Use for every query; never hand-write the Go that runs SQL.
---

# Write a query

SQL lives in `internal/store/queries/*.sql`. The Go that runs it is generated
by sqlc into `internal/store/db` and **committed**. Never hand-write a
`pool.Query` outside that package, and never edit a generated file.

## Step 1 — write the SQL

One file per table or per area. The comment above each query is the whole API:

```sql
-- name: CreateTeam :one
INSERT INTO teams (name) VALUES ($1) RETURNING *;

-- name: ListTeams :many
SELECT * FROM teams ORDER BY created_at DESC;

-- name: DeleteTeam :exec
DELETE FROM teams WHERE id = $1;
```

| Annotation  | Returns             | Missing row              |
| ----------- | ------------------- | ------------------------ |
| `:one`      | `(T, error)`        | `pgx.ErrNoRows`          |
| `:many`     | `([]T, error)`      | empty slice, `nil` error |
| `:exec`     | `error`             | `nil` — no error         |
| `:execrows` | `(int64, error)`    | `0` — no error           |

The `:exec` row surprises people: `DELETE` matching nothing is not a failure in
SQL. A caller that needs "did it exist" has to read first.

### An upsert that has to return the row it did not write

`:execrows` answers "did anything happen"; it cannot answer "which row". The
moment a caller needs the id — a child row to hang off it, an event to emit —
`ON CONFLICT DO NOTHING` looks like a problem, because it returns no row at
all and `:one` becomes `pgx.ErrNoRows`.

The usual fix is a `DO UPDATE` that changes nothing so the row is always
returned, and it has a cost that does not show up in a test:

```sql
ON CONFLICT (channel_id, external_id)
DO UPDATE SET external_id = EXCLUDED.external_id   -- a no-op, and not free
RETURNING id;
```

Postgres is MVCC, so an `UPDATE` writes a **new row version** whether or not
any value changed. Measured on 18.6, 200 conflicting inserts of a single row:

```text
DO UPDATE   heap 16 kB      (200 dead tuples for 200 no-op writes)
DO NOTHING  heap 8192 bytes (nothing written at all)
```

`(xmax = 0) AS inserted` in the `RETURNING` distinguishes a real insert from
the no-op update, so the information is recoverable — but you have paid for a
write to get it.

Ask instead **whether the caller needs the id on the conflicting path at all.**
Usually the conflict *is* the answer: a webhook replay has nothing new to
attach, an idempotent job has already run. Then `DO NOTHING ... RETURNING id`
is exactly right, and `ErrNoRows` is not an error to handle but the second
answer to the question:

```go
	messageID, err := q.InsertMessage(ctx, arg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			continue        // already delivered; nothing below applies
		}
		return fmt.Errorf(...)
	}
```

Reach for `DO UPDATE` only when the conflicting path genuinely has work to do.
Say which it is in the query comment, because the next reader will see
`DO NOTHING` with `:one` and assume it is a bug.

Prefer `RETURNING *` on an insert. It costs nothing and hands back the
defaulted columns — `id`, `created_at` — so the caller never re-reads. With
`RETURNING id` the rest come back as zero values and nobody notices.

Use `sqlc.arg(name)` instead of `$1` when a query has enough parameters that
positions stop being readable.

## Step 2 — generate

```sh
make generate-sql     # or make generate, the umbrella
```

The schema comes from the goose migrations themselves — sqlc understands the
`-- +goose Down` marker and ignores everything under it — so there is no second
copy of the schema. A new column is available to queries as soon as its
migration is written.

Commit the generated files **in the same commit** as the `.sql` change. CI runs
`make generate-check`, which regenerates and fails if the result differs.

### A nullable aggregate becomes `interface{}`

`min`, `max` and `sum` return null over no rows, and sqlc types that as
`interface{}` — which compiles, and hands the caller something it has to
assert:

```sql
-- name: DeletedObjectBacklog :one
SELECT count(*) AS outstanding, min(deleted_at) AS oldest FROM deleted_objects;
```

```go
type DeletedObjectBacklogRow struct {
	Outstanding int64
	Oldest      interface{}   // no
}
```

Casting it — `min(deleted_at)::timestamptz` — makes sqlc believe it cannot be
null, and the scan then fails on the empty table, which is the ordinary case.

Ask for the row instead of the aggregate, and carry the count on it with a
window function:

```sql
-- name: DeletedObjectBacklog :many
SELECT count(*) OVER () AS outstanding, deleted_at AS oldest
FROM deleted_objects
ORDER BY deleted_at
LIMIT 1;
```

`:many` returning nought or one row, both fields typed, one round trip. The
empty backlog is an empty slice rather than a null nobody typed. **When an
aggregate wants to be nullable, return the row it came from.**

## Step 3 — types

`sqlc.yaml` maps `uuid` to `uuid.UUID` and `timestamptz` to `time.Time`.
Without those overrides every caller would be unwrapping `pgtype.UUID`.

Both apply to `NOT NULL` columns. **A nullable column of either type needs its
own override** with `nullable: true`, or sqlc falls back to the pgtype wrapper
for that field only — which is correct, just inconsistent. Add the override
when you add the column.

## Step 4 — one write or several?

A single query is already atomic. Two or more writes that must agree go through
`Store.InTx`:

```go
err := s.InTx(ctx, func(q *db.Queries) error {
	team, err := q.CreateTeam(ctx, "platform")
	if err != nil {
		return err
	}
	return q.CreateMember(ctx, team.ID, userID)
})
```

Return an error anywhere and nothing was written.

Two rules, both of which fail silently rather than loudly:

- **Use `q`, never the outer `s`.** `s.CreateTeam` inside the callback runs on
  the pool, outside the transaction, and commits even when the rest rolls back.
  The callback takes `*db.Queries` precisely so the transactional handle is the
  only one in scope — do not reach around it.
- **Do not nest `InTx`.** It holds one pooled connection; an inner call takes a
  second connection and starts an unrelated transaction.

## Step 5 — bulk work is a different tool

`InTx` gives atomicity, not throughput. A thousand inserts inside it is still a
thousand round trips.

```sql
-- name: CreateTeams :batchexec
INSERT INTO teams (name) VALUES ($1);
```
One round trip, per-statement errors through `results.Exec(func(i int, err error))`.
Must be `Close()`d.

```sql
-- name: BulkInsertTeams :copyfrom
INSERT INTO teams (name) VALUES ($1);
```
The `COPY` protocol — much faster still, but no `RETURNING` and no
`ON CONFLICT`, because it is not really an `INSERT`.

Both compose with `InTx` rather than replacing it. **The trigger to reach for
one is writing a loop that calls a query.** Do not add either speculatively: a
generated query with no caller is committed code at 0% coverage that a reviewer
cannot justify.

## Step 6 — test it

Follow `test-with-postgres`. Every query needs a real round-trip; a fake tests
the fake.

What the query tests here are built to catch:

- **Ordering.** Insert rows, then move `created_at` so the intended order is
  the *reverse* of insertion order before asserting. Three rows coming back in
  insertion order is what an unordered scan returns anyway, so the naive test
  passes with no `ORDER BY` at all.
- **Missing rows.** `errors.Is(err, pgx.ErrNoRows)`, not a nil check.
- **Transactions.** Assert from *outside* the transaction, through
  `s.pool`, that an uncommitted write is invisible. Without that, a broken
  `InTx` that writes through the pool passes every other test.
- **Rollback on panic**, not only on error return.
- **The three error paths of a `:many`.** Each returns `nil, err`, so a query
  that half-failed must never surface as a short list and a nil error. Reach
  them by breaking the database under the query — the test's schema is its own
  and is dropped afterwards, so mangling it is free:

  | Branch | How |
  | --- | --- |
  | `Query` fails | `s.Close()`, then call the query |
  | `Scan` fails | `ALTER COLUMN id TYPE text`, insert a non-uuid |
  | `rows.Err()` fails | replace the table with a view that divides by zero at row 500 — Postgres streams it, so the error arrives after 499 good rows |

  Only the third one proves a *partial* result becomes an error, which is the
  case a caller could otherwise be handed silently.
- **Connection release.** Loop `InTx` more times than `maxConns` and assert
  `s.pool.Stat().AcquiredConns() == 0`. Give the test a bounded context: a leak
  does not make `Begin` fail, it makes `Begin` block, and without a deadline
  the suite hangs instead of failing.

Then break the query — remove the `ORDER BY`, swap `q` for `s` — and confirm
the test fails. If it still passes, it is measuring nothing.

### Mutate the SQL without changing the generated signature

sqlc derives the Go parameters from the SQL, so deleting a `WHERE` clause
deletes an argument and every call site stops compiling. That is a build
failure, not a caught mutation — the test never ran, and anything grepping for
`FAIL` will read it as success.

Keep the parameter and destroy the filter instead:

```sql
WHERE message_id = $1                    -- the query
WHERE (message_id = $1) IS NOT NULL      -- always true, still one argument
```

The same trick covers a filter you want neutralised anywhere: compare it and
throw the comparison away. For an `ORDER BY` or a `LIMIT` there is no
signature to preserve, so delete those outright.

Regenerate between the mutation and the test — `make generate` — or the
committed Go still holds the original query and the mutation is invisible.

## Step 6b — a query only its own tests call

`unused` cannot see this: every sqlc query is an exported method on `*Queries`,
so it always looks used. `GetAttachmentWithTeam` was written to join for a team
id, kept its tests when a column made the join unnecessary, and was called by
nothing else for a whole change.

```sh
grep -rn "GetAttachmentWithTeam" internal/ cmd/ | grep -v _test.go | grep -v db/
```

One line — the generated method — means the query is dead. Delete it and adapt
the tests to whatever answers the question now; a query kept "because it has
tests" is tests keeping code alive rather than the other way round.

Worth running over any query whose reason for existing was removed by the same
change.

## Step 7 — verify

```sh
cd apps/brain
make check db=postgres
```

`make check` includes `generate-check`, so it fails if the committed Go does
not match the SQL. Pass `db=` or every query test skips and the generated
package reports 0% while looking untested.
