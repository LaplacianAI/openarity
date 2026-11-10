---
name: test-with-postgres
description: Write a test that needs a real database — queries, migrations, transactions, anything sqlc-generated. Covers skipping when no database is available, isolating each test in a database copied from a migrated template, which tests may be parallel, and how to check that a test would actually fail. Use for every test that talks to Postgres.
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

Locally, pass the database name and the Makefile builds the DSN:

```sh
make testdb db=openarity_test    # once per machine — the database must exist
make check  db=openarity_test
make cover-html db=openarity_test
make check  db=postgres          # or skip the setup and use what is there
```

`host`, `port`, `user` and `sslmode` take overrides for a remote server.

**`port` defaults to 5432, which is the trap.** With a Postgres installed on
the machine as well as one in compose, `db=postgres` reaches the installed one
and the suite tests a server nobody chose. Pass `port=15432` for the compose
database. `internal/store` has a `TestMain` that refuses anything below
Postgres 18 — the version `schema_test.go`'s `SQLSTATE 23001` needs — and names
the address it reached, so this now fails loudly instead of surfacing as one
baffling assertion. It is the only place that fails rather than skips, and only
because a skip there would be the same silence in a different colour.

There is no default `db`, and there must not be one. The skip triggers on the
variable being **empty**, not on the server being unreachable — so a default
would turn "no Postgres installed" from a skip into a wall of failures for
every contributor who does not happen to run one on 5432.

The helper is duplicated per package rather than shared. It is six lines, and a
`testdb` package would be a dependency crossing every boundary in the tree.

## Step 2 — a database of its own, copied from a migrated template

In `internal/store`, a test that needs the schema calls `queryStore(t)`
(`testdb_test.go`). The first call in a process migrates one template
database, `brain_tmpl_<run>`; every call then runs `CREATE DATABASE
brain_t_<run>_<n> TEMPLATE brain_tmpl_<run>` and drops it with `WITH (FORCE)`
when the test ends. `TestMain` drops the template.

Why not migrate per test, as this package once did: `Migrate` takes an
advisory lock with one fixed id, and an advisory lock covers the whole
database, so every test's migration queued behind the last and the package
passed eight minutes. A copy takes about 0.2 s and no lock anyone waits on.

`<run>` is random per `go test` process, so two runs against one server never
drop each other's databases. Test connections set `synchronous_commit=off`:
the database is thrown away, and on a containerised Postgres waiting for each
commit to reach the disk was most of the time.

A test of migrations themselves needs an empty database, not a migrated one:
`migrationStore(t)` gives it one, `brain_m_<run>_<n>`, dropped the same way.
A database rather than a schema because the migration lock is an advisory
lock, and an advisory lock covers a whole database: two tests migrating
schemas of one database, or two runs, would wait on each other's lock.

The test user needs `CREATEDB`; the compose and CI `postgres` user is a
superuser.

## Step 3 — `t.Parallel()` on a test with a database of its own

A `queryStore` test shares nothing with any other, so it calls `t.Parallel()`.
Leave it off a test of advisory locks (`lockStore`), which share the server's
lock table.

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
make check                # skips the database tests
make check db=postgres    # runs them
```

Both must pass. A test that only works with a database set up is fine; one that
*breaks* the suite without one is not.

Read a coverage report only from the second form. Without a database, `serve`,
`migrateUp` and every generated query report 0% — they are not untested, their
tests skipped — and the total drops from 96.9% to 70.3%. `make cover` prints a
warning when the variable is unset for this reason.
