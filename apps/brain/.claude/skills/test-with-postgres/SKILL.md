---
name: test-with-postgres
description: Write a test that needs a real database — queries, migrations, transactions, anything sqlc-generated. Covers skipping when no database is available, isolating each test in its own schema, why these tests cannot be parallel, and how to check that a test would actually fail. Use for every test that talks to Postgres.
---

# Write a test that needs Postgres

Most tests here need no database. Reach for this only when the thing under test
genuinely talks to one — a query, a migration, a transaction boundary.

Everything else — parsing, validation, handlers, middleware — should be tested
without one. `parse` and `LogRequests` need nothing set up, and that is why
their tests run in milliseconds.

## Step 1 — skip when there is no database

```go
func liveDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}
```

**Skip, never fail.** `make test` has to stay useful with nothing running. CI
sets the variable from a service container in `.github/workflows/ci.yml`, so
these tests always run there.

Locally:

```sh
export BRAIN_TEST_POSTGRES_DSN="postgres://$USER@127.0.0.1:5432/postgres?sslmode=disable"
```

The helper is duplicated per package rather than shared. It is six lines, and a
`testdb` package would be a dependency crossing every boundary in the tree.

## Step 2 — one schema per test

Never share tables between tests. Create a schema, point the DSN at it with
`search_path`, drop it afterwards:

```go
schema := "brain_test_" + strings.ToLower(t.Name())
```

```go
u, _ := url.Parse(dsn)
q := u.Query()
q.Set("search_path", schema)
u.RawQuery = q.Encode()
```

Everything then lands inside that schema — your tables *and* goose's
`goose_db_version` — so the drop takes all of it.

Use `t.Cleanup`, and wrap the context in `context.WithoutCancel(t.Context())`:
a failing test cancels its context, and cleanup still has to run.

```go
t.Cleanup(func() {
	ctx := context.WithoutCancel(t.Context())
	// ... DROP SCHEMA ... CASCADE
})
```

Naming the schema after `t.Name()` means a leaked schema tells you which test
leaked it.

## Step 3 — no `t.Parallel()`

Database tests share one server, one set of advisory locks, and often one
schema-creation path. `t.Parallel()` on two tests that migrate will have them
fighting over the migration lock and blocking each other.

Everything that needs a database runs serially. Everything that does not gets
`t.Parallel()`.

## Step 4 — assert both directions

The whole package asserts failure — "Ping errors when the database is down",
"an invalid DSN is rejected". Without at least one success case, a `Ping` that
*always* returned an error would pass the entire suite.

Every database-backed feature needs one test that proves it works when things
are fine, not only that it complains when they are not.

## Step 5 — prove the test can fail

**A test that passes whether or not the feature exists is worse than no test**,
because it reads like coverage.

This bit us once. A test raced two goroutines to prove the migration lock
worked. It passed with `WithSessionLocker` removed — the migration finished
before the second goroutine looked at the version table, so the two never
overlapped.

The fix was to make the race deterministic: hold the lock from the test itself,
then assert `Migrate` blocks.

```go
holder, _ := s.pool.Acquire(t.Context())
holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID)

go func() { done <- s.Migrate(ctx) }()

select {
case <-done:
	t.Fatal("Migrate ran while the lock was held")
case <-time.After(500 * time.Millisecond):
	// correct: still blocked
}
```

So: after writing a test for a guard, **temporarily remove the guard and run
it**. If it still passes, the test is measuring nothing. Put the guard back.

The same trick applies to any concurrency test — if the operation is fast, the
goroutines never actually overlap and the race you meant to test never happens.

## Step 6 — timing

No `time.Sleep` to wait for something. Poll with a deadline that fails the
test:

```go
deadline := time.Now().Add(5 * time.Second)
for time.Now().Before(deadline) {
	// ... check ...
	time.Sleep(10 * time.Millisecond)
}
t.Fatalf("never became ready")
```

A sleep long enough to be reliable is long enough to be slow; short enough to
be fast is flaky in CI.

`time.After` inside a `select` is fine — that is asserting something did *not*
happen within a window, which is different.

## Step 7 — what the database gives you that a fake cannot

Keep in mind which failures only appear against a real server:

- constraint violations, foreign keys, `ON CONFLICT`
- transaction and lock behaviour
- what the SQL actually does — a fake tests your mock, not your query

And which ones do not, so you do not chase them:

- **`pg_hba.conf` depends on the environment.** A test asserting that a wrong
  password is rejected passes in CI and fails on a machine using `trust` auth.
  One was written and deleted for exactly this. Never assert on server
  configuration.

## Step 8 — verify

```sh
cd apps/brain
make check                                    # skips the database tests
BRAIN_TEST_POSTGRES_DSN=... make check        # runs them
```

Both must pass. A test that only works with a database set up is fine; one that
*breaks* the suite without one is not.
