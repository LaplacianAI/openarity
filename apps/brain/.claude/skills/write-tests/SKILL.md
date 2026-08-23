---
name: write-tests
description: Write a Go test for the brain — any package, any layer. Covers what to name it, when t.Parallel() is allowed, contexts and ports and timing, how to fake a dependency, and the check that the test would actually fail. Use for every test; read test-with-postgres as well if it touches a database.
---

# Write a test

Every change ships with tests. This is the house style; `test-with-postgres`
covers the extra rules for anything that talks to a database.

## Step 1 — name it after the behaviour

```go
func TestReadyzReturns503WhenTheDatabaseIsDown(t *testing.T)
func TestHealthzIgnoresTheDatabase(t *testing.T)
func TestMigrateWaitsForTheSessionLock(t *testing.T)
```

Not `TestReadyz2`, not `TestReadyzError`. The name is what a reader sees in a
CI log, and it should say what broke without opening the file.

Tests mirror source files: `validate.go` has `validate_test.go`. When a set of
tests covers one behaviour across several files, a file named for the
behaviour (`lifecycle_test.go`) is better than spreading them.

## Step 2 — write a comment saying why the test exists

Not what it does — the code says that. Why it would be a bad idea to delete it:

```go
// The pool is lazy, so nothing but Ping proves the database is there. Without
// this, a wrong host and a stopped server both look like success.
```

If you cannot write that sentence, the test may not be worth having.

## Step 3 — `t.Parallel()` where it is safe, nowhere else

| Test touches                            | `t.Parallel()` |
| --------------------------------------- | -------------- |
| pure functions, parsing, validation      | yes            |
| an `httptest` handler, a fake            | yes            |
| `t.Setenv`                               | **no** — Go panics |
| a real database                          | **no** — shared server and locks |
| a real listener on a fixed port          | **no**         |

## Step 4 — contexts, ports, timing

- **`t.Context()`, never `context.Background()`.** It is cancelled when the
  test ends, so a leaked goroutine dies with the test instead of the suite.
- **Anything under test takes its context and its writer as parameters.** A
  function that builds its own context cannot be cancelled by a test, and one
  that writes to `os.Stdout` cannot be observed. Both turn a fast test into a
  ten-minute timeout — this happened here, and it is why `run(ctx, out, args)`
  has that signature.
- **Reserve a real ephemeral port; never hardcode one and never use port 0.**
  Bind `127.0.0.1:0`, read `l.Addr()`, close it, use the address. `freeAddr` in
  `cmd/brain` and `internal/server` does this. `checkHostPort` rejects port 0,
  so the obvious shortcut fails.
- **`noctx` bans the context-free constructors.** Use
  `httptest.NewRequestWithContext`, `(*net.ListenConfig).Listen`,
  `exec.CommandContext`.
- **No `time.Sleep` to wait for something.** Poll with a deadline that fails
  the test. A sleep long enough to be reliable is long enough to be slow, and
  short enough to be fast is flaky in CI. `time.After` inside a `select` is
  fine — asserting something did *not* happen in a window is a different thing.
- **Bound anything that can block.** A leaked connection does not make the next
  call fail, it makes it block; without a deadline the suite hangs instead of
  failing, and a hang is far worse to debug than a failure.

## Step 5 — fakes

Only where an interface already exists for a real reason. `fakePinger` exists
because `server.Server` takes a `Pinger` — the interface came first, from the
need to keep `internal/server` ignorant of Postgres.

A fake must:

- **honour the context** it is given, or it hides cancellation bugs
- **count its calls**, so a test can assert something was *not* called —
  `TestHealthzIgnoresTheDatabase` asserts `Ping` ran zero times, which is the
  whole point of that endpoint
- **be race-safe** — a mutex around the counter; the suite runs with `-race`

Do not fake what you can drive for real. Handlers go through `httptest`, not a
fake `ResponseWriter`.

## Step 6 — assert both directions

Most tests here assert failure: "Ping errors when the database is down", "an
invalid DSN is rejected". Without at least one success case, a function that
*always* returned an error passes the entire suite.

Assert on behaviour, not on strings you do not own:

```go
if !errors.Is(err, pgx.ErrNoRows) {      // yes
if err.Error() != "no rows in result set" {   // no — pgx owns that string
```

Checking that *our* error names the offending value is fine, and worth doing —
`strings.Contains(err.Error(), "worker")` proves the message is useful.

## Step 7 — prove the test can fail

**A test that passes whether or not the feature exists is worse than no test**,
because it reads like coverage.

Break the thing, run the test, put it back. Real examples from this repo:

| Mutation                                  | Test that caught it            |
| ----------------------------------------- | ------------------------------ |
| remove `ORDER BY created_at DESC`          | ordering test                  |
| pass the pool instead of the transaction   | three transaction tests        |
| delete `defer tx.Rollback`                 | connection-release test        |
| remove `WithSessionLocker`                 | migration lock test            |

That last one is the cautionary tale: the original version raced two goroutines
and **passed with the locker removed**, because the migration finished before
the second goroutine looked. The fix was to make the race deterministic — hold
the lock from the test, then assert the operation blocks.

If the operation is fast, goroutines never actually overlap and the race you
meant to test never happens.

### Put a compile gate in front of the mutation

A mutation that does not compile prints `FAIL` from the build, and anything
grepping for `FAIL` reads that as "the test caught it". It caught nothing —
the test never ran. Removing a guard often removes the last use of a variable
or an import, so this is the common case, not the rare one.

```sh
go build ./... || { echo "BUILD-FAIL"; }   # a distinct outcome, not a pass
go test ./the/package -count=1
```

`-count=1` as well: the test cache will happily serve the result from before
the mutation.

### Two patterns that survive mutation and look tested

Both escaped a first pass on the sessions work, and both are invisible in a
coverage report because the lines *do* execute — just never with the value
that matters.

**A field with an absent and a present form, tested only absent.** The fixture
for `sent_at` had no timestamp, so nilling the field changed nothing. A test
asserting "absent stays absent" says nothing about whether a present one
survives. Test both forms explicitly.

**Two near-identical types, one tested.** Sessions and messages each have a
cursor, ordering on different columns. The paging test covered the message
cursor; breaking the session cursor changed no result. The lines in the
untested one are still executed by its sibling's code path, so nothing reports
a gap.

The rule both give you: coverage says which lines ran, never which values
reached them. For any nullable field and any pair of parallel types, write the
second test.

## Step 8 — coverage

- **Never delete error handling to raise a number.** When a branch is
  genuinely unreachable, say so in a comment and pin the assumption with a test
  that fails if it stops holding — see `TestSessionLockerCannotFailWithoutOptions`.
- **Never read a coverage report produced without a database.** Database tests
  skip, so `serve`, `migrateUp` and every generated query read 0% and the total
  drops from 96.9% to 70.3%. That looks like a coverage problem and is not one.

```sh
make cover db=postgres
make cover-html db=openarity_test    # after make testdb db=openarity_test
```

`make cover` warns when no database is named.

## Step 9 — verify

```sh
cd apps/brain
make check              # must pass with nothing running
make check db=postgres  # must pass with a database
```

Both. A test that only works with a database is fine; one that *breaks* the
suite without one is not.
