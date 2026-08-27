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

| Annotation | Returns             | Missing row              |
| ---------- | ------------------- | ------------------------ |
| `:one`     | `(T, error)`        | `pgx.ErrNoRows`          |
| `:many`    | `([]T, error)`      | empty slice, `nil` error |
| `:exec`    | `error`             | `nil` — no error         |

That last row surprises people: `DELETE` matching nothing is not a failure in
SQL. A caller that needs "did it exist" has to read first.

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

## Step 7 — verify

```sh
cd apps/brain
make check db=postgres
```

`make check` includes `generate-check`, so it fails if the committed Go does
not match the SQL. Pass `db=` or every query test skips and the generated
package reports 0% while looking untested.
