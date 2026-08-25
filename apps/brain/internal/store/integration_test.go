package store

import (
	"context"
	"fmt"
	"net"
	"os"
	// Aliased: queries_test.go already declares a package-level exec helper.
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The oldest Postgres these tests can run against, which is not the oldest
// Postgres the brain can run against. schema_test.go asserts SQLSTATE 23001,
// and 18 is the first release that raises it: 13 through 17 report the same
// refusal as a plain foreign key violation, 23503. Measured against every one
// of those releases rather than read from a changelog.
//
// The brain itself needs 13, for gen_random_uuid() in the first migration —
// also measured, by applying every migration to 11 through 18. The only code
// production matches on is 23505, which has been stable throughout. So this
// number belongs to the test suite alone, and saying so is half the point of
// the message below.
const minServerVersionNum = 180000

// TestMain refuses a server too old before any test runs, rather than letting
// one confusing assertion carry the news.
//
// The bug this exists for was not a wrong assertion, it was a suite that ran
// green against a database nobody meant to use: `make check db=postgres`
// reached a Postgres 14 installed on the host's 5432 while the compose
// database sat on 15432, and thirty deletes raised a code the test did not
// expect. Failing here rather than skipping is deliberate — a skip would have
// been the same silence in a different colour.
func TestMain(m *testing.M) {
	if dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN"); dsn != "" {
		if err := checkServerVersion(dsn, minServerVersionNum); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// checkServerVersion connects once and reports what it found. The message
// names the server as well as the version, because the failure it is built for
// is pointing at the wrong one — the DSN is well-formed, the port is a
// default, and two databases answer on the same machine.
//
// floor is a parameter rather than the constant so the refusal can be tested
// against whatever Postgres is actually running, by raising the floor past it.
// Reaching the branch any other way would mean keeping an old server around.
func checkServerVersion(dsn string, floor int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("BRAIN_TEST_POSTGRES_DSN: connecting to %s: %w", serverAddr(dsn), err)
	}
	defer conn.Close(ctx)

	var (
		num     int
		version string
	)
	if err := conn.QueryRow(ctx,
		`SELECT current_setting('server_version_num')::int, current_setting('server_version')`,
	).Scan(&num, &version); err != nil {
		return fmt.Errorf("BRAIN_TEST_POSTGRES_DSN: reading the version of %s: %w", serverAddr(dsn), err)
	}

	if num < floor {
		return fmt.Errorf(`BRAIN_TEST_POSTGRES_DSN points at PostgreSQL %s on %s.
These tests need 18 or newer: schema_test.go asserts SQLSTATE 23001, and 18 is
the first release that raises it. The brain itself runs on 13 or newer — this
floor belongs to the test suite, not to the product.
If a compose database is up, this is probably not it. Try:
    make check db=openarity_test port=15432`, version, serverAddr(dsn))
	}
	return nil
}

// serverAddr is the host and port of a DSN and nothing else. A DSN carries a
// password, and this string goes to stderr and into CI logs.
func serverAddr(dsn string) string {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "the configured server"
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
}

// liveDSN returns a DSN for a real Postgres, or skips. CI sets
// BRAIN_TEST_POSTGRES_DSN from its service container; locally, export it
// against whatever compose brings up. Skipping rather than failing keeps
// `make test` useful with nothing running — the version check above only
// applies once something is set.
func liveDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BRAIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BRAIN_TEST_POSTGRES_DSN is not set")
	}
	return dsn
}

// The whole point of Ping: against a database that is actually there, it must
// succeed. Every other test in this package asserts the failure direction, so
// without this one a Ping that always errored would still pass the suite.
func TestPingSucceedsAgainstRealPostgres(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping against a live database: %v", err)
	}
}

// The pool must survive more concurrent callers than it has connections.
// MaxConns is 10; anything past that queues rather than erroring.
func TestPoolQueuesBeyondMaxConns(t *testing.T) {
	t.Parallel()

	s, err := New(t.Context(), liveDSN(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	errs := make(chan error, maxConns*3)
	for range maxConns * 3 {
		go func() { errs <- s.Ping(ctx) }()
	}
	for range maxConns * 3 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Ping failed: %v", err)
		}
	}
}

// serverAddr must not leak the password, and must not turn a DSN it cannot
// parse into a panic — it runs on the path that reports a connection failure,
// where a malformed DSN is one of the likely causes.
func TestServerAddrNamesTheHostWithoutTheCredentials(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ dsn, want string }{
		"with a password": {"postgres://alice:hunter2@db.internal:15432/x?sslmode=disable", "db.internal:15432"},
		"without one":     {"postgres://alice@127.0.0.1:5432/x?sslmode=disable", "127.0.0.1:5432"},
		"unparseable":     {"postgres://alice@db:notaport/x", "the configured server"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := serverAddr(tc.dsn)
			if got != tc.want {
				t.Errorf("serverAddr(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
			if strings.Contains(got, "hunter2") {
				t.Errorf("serverAddr leaked the password: %q", got)
			}
		})
	}
}

// The server that is actually configured must pass, or every other test in
// this package is being skipped for a reason nobody looked at.
func TestCheckServerVersionAcceptsTheConfiguredServer(t *testing.T) {
	t.Parallel()

	if err := checkServerVersion(liveDSN(t), minServerVersionNum); err != nil {
		t.Fatalf("checkServerVersion against the configured server: %v", err)
	}
}

// The refusal has to name the version and the server, or it sends the reader
// to look at their schema instead of their DSN — which is exactly what
// happened the day this was written.
func TestCheckServerVersionRefusesAServerBelowTheFloor(t *testing.T) {
	t.Parallel()

	dsn := liveDSN(t)

	// Above every Postgres that exists, so the branch is reached against
	// whatever is running rather than by keeping an old server around.
	err := checkServerVersion(dsn, 999999)
	if err == nil {
		t.Fatal("checkServerVersion accepted a server below the floor")
	}

	got := err.Error()
	for _, want := range []string{serverAddr(dsn), "18 or newer", "SQLSTATE 23001", "13 or newer"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, got)
		}
	}
}

// A connection failure must name the server it could not reach, and must not
// read as a version problem.
func TestCheckServerVersionReportsAConnectionFailure(t *testing.T) {
	t.Parallel()

	// Port 1 is reserved and nothing listens on it, so the connection is
	// refused rather than left to time out.
	const dead = "postgres://nobody@127.0.0.1:1/x?sslmode=disable"

	err := checkServerVersion(dead, minServerVersionNum)
	if err == nil {
		t.Fatal("checkServerVersion against a dead address returned no error")
	}

	got := err.Error()
	if !strings.Contains(got, "127.0.0.1:1") || !strings.Contains(got, "connecting to") {
		t.Errorf("the failure never names the server it could not reach:\n%s", got)
	}
	if strings.Contains(got, "18 or newer") {
		t.Errorf("a connection failure was reported as a version problem:\n%s", got)
	}
}

// TestMain is the only thing that applies the check to the package, and every
// other test here calls checkServerVersion directly — so deleting the call in
// TestMain would leave them all green. Nothing warns about that.
//
// Run this binary again as a subprocess against a server it cannot reach, and
// assert two things: it exits non-zero, and no test ran first. An unreachable
// address rather than an old one, because there is no guarantee an old
// Postgres exists on the machine running this.
func TestMainRefusesBeforeRunningAnything(t *testing.T) {
	t.Parallel()

	cmd := osexec.CommandContext(t.Context(), os.Args[0],
		"-test.run", "^TestPingSucceedsAgainstRealPostgres$")
	cmd.Env = append(os.Environ(),
		"BRAIN_TEST_POSTGRES_DSN=postgres://nobody@127.0.0.1:1/x?sslmode=disable")

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the subprocess ran its tests against an unreachable server:\n%s", out)
	}
	if !strings.Contains(string(out), "connecting to 127.0.0.1:1") {
		t.Errorf("the subprocess failed for some other reason:\n%s", out)
	}
	if strings.Contains(string(out), "TestPingSucceedsAgainstRealPostgres") {
		t.Errorf("a test ran before the check refused:\n%s", out)
	}
}
