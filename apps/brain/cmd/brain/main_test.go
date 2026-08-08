package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// freeAddr returns an address nothing is listening on. Config rejects port 0,
// so a real port has to be reserved and released.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

// stubEnv points every listener and the database at addresses nothing is
// serving. run therefore reaches the Postgres check and stops there, which is
// as far as it can get without a real database — see the note in
// TestRunFailsWhenPostgresIsUnreachable.
func stubEnv(t *testing.T) (apiBind string) {
	t.Helper()

	apiBind = freeAddr(t)
	t.Setenv("OPENARITY_API_BIND", apiBind)
	t.Setenv("OPENARITY_WEBHOOK_BIND", freeAddr(t))
	t.Setenv("OPENARITY_POSTGRES_DSN",
		"postgres://user:hunter2@"+freeAddr(t)+"/openarity?sslmode=disable")

	return apiBind
}

// The startup log must be written before anything can fail, so a process that
// dies during startup still says what it was trying to be.
func TestRunLogsStartupBeforeDialling(t *testing.T) {
	stubEnv(t)

	var buf bytes.Buffer
	if err := run(t.Context(), &buf, nil); err == nil {
		t.Fatal("run succeeded with no database")
	}
	if !strings.Contains(buf.String(), "development") {
		t.Errorf("startup log missing or written too late: %q", buf.String())
	}
}

// The pool is lazy, so nothing but Ping proves the database is there. Without
// that check the process comes up, passes its probe and serves every request
// into a connection error.
func TestRunFailsWhenPostgresIsUnreachable(t *testing.T) {
	stubEnv(t)

	var buf bytes.Buffer
	err := run(t.Context(), &buf, nil)
	if err == nil {
		t.Fatal("run started with an unreachable database")
	}
	if !strings.Contains(err.Error(), "ping") && !strings.Contains(err.Error(), "database") {
		t.Errorf("error does not say the database is the problem: %v", err)
	}
	if strings.Contains(buf.String(), "Listening") {
		t.Errorf("listeners came up before the database check: %s", buf.String())
	}
}

// The writer parameter is the only output path. Anything reaching the real
// stdout is a hardcoded os.Stdout that tests cannot observe.
func TestRunWritesNothingToStdout(t *testing.T) {
	stubEnv(t)

	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()

	var buf bytes.Buffer
	_ = run(t.Context(), &buf, nil)

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	leaked, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(leaked) != 0 {
		t.Errorf("run wrote to os.Stdout instead of its writer: %q", leaked)
	}
}

// A config error must reach the caller, and must not be preceded by a
// half-written line. Partial output before a failure is how a startup log
// ends up claiming a listener came up that never did.
func TestRunReturnsErrorAndWritesNothing(t *testing.T) {
	stubEnv(t)
	t.Setenv("OPENARITY_API_BIND", "no-port-here")

	var buf bytes.Buffer
	if err := run(t.Context(), &buf, nil); err == nil {
		t.Fatal("run accepted an invalid API_BIND")
	}
	if buf.Len() != 0 {
		t.Errorf("run wrote output despite failing: %q", buf.String())
	}
}

// The DSN password must reach neither the log nor the returned error. The
// error path matters most: it is printed to stderr by main and is the one
// place redaction is easiest to forget.
func TestRunDoesNotLeakPassword(t *testing.T) {
	stubEnv(t)

	var buf bytes.Buffer
	err := run(t.Context(), &buf, nil)
	if err == nil {
		t.Fatal("run started with an unreachable database")
	}

	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("startup log leaked the Postgres password: %s", buf.String())
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("returned error leaked the Postgres password: %v", err)
	}
}

// main must turn a startup error into a non-zero exit code. os.Exit cannot be
// observed in-process, so re-exec the test binary and inspect the real exit.
// The subprocess is the same coverage-instrumented binary and its counters are
// merged, so this is also the only thing covering main. Drop it and main goes
// to 0%.
func TestMainExitsNonZeroOnBadConfig(t *testing.T) {
	if os.Getenv("BRAIN_TEST_SUBPROCESS") == "1" {
		main()
		return
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestMainExitsNonZeroOnBadConfig")
	cmd.Env = append(os.Environ(),
		"BRAIN_TEST_SUBPROCESS=1",
		"OPENARITY_API_BIND=no-port-here",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("main exited 0 on an invalid config:\n%s", out)
	}
	if !strings.Contains(string(out), "brain:") {
		t.Errorf("main did not report the error on stderr:\n%s", out)
	}
}
