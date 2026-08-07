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

// Unused binds so a test never collides with a real brain on 21120, and a
// context that is already cancelled so run returns instead of serving.
func stubEnv(t *testing.T) (context.Context, string) {
	t.Helper()

	apiBind := freeAddr(t)
	t.Setenv("OPENARITY_API_BIND", apiBind)
	t.Setenv("OPENARITY_WEBHOOK_BIND", freeAddr(t))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx, apiBind
}

// The startup log is the only evidence of what the process decided to be. It
// has to carry the environment and the address it actually bound, written to
// the writer run was handed.
func TestRunLogsStartup(t *testing.T) {
	ctx, apiBind := stubEnv(t)

	var buf bytes.Buffer
	if err := run(ctx, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"development", apiBind} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("startup log never mentioned %q: %s", want, buf.String())
		}
	}
}

// A cancelled context is a clean stop, not a failure. ListenAndServe returns
// http.ErrServerClosed on shutdown; if that is not mapped to nil, every
// SIGTERM exits 1 and Kubernetes records a crash on every rolling update.
func TestRunReturnsNilOnContextCancel(t *testing.T) {
	ctx, _ := stubEnv(t)

	var buf bytes.Buffer
	if err := run(ctx, &buf); err != nil {
		t.Errorf("run after a clean shutdown = %v, want nil", err)
	}
}

// The writer parameter is the only output path. Anything reaching the real
// stdout is a hardcoded os.Stdout that tests cannot observe.
func TestRunWritesNothingToStdout(t *testing.T) {
	ctx, _ := stubEnv(t)

	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()

	var buf bytes.Buffer
	if err := run(ctx, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
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
	ctx, _ := stubEnv(t)
	t.Setenv("OPENARITY_API_BIND", "no-port-here")

	var buf bytes.Buffer
	err := run(ctx, &buf)
	if err == nil {
		t.Fatal("run accepted an invalid API_BIND")
	}
	if buf.Len() != 0 {
		t.Errorf("run wrote output despite failing: %q", buf.String())
	}
}

// Config redaction is only useful if the startup path actually goes through
// it. Assert on run, not on Config.String, so a future fmt.Fprintf with %+v
// is caught here.
func TestRunDoesNotLeakPassword(t *testing.T) {
	ctx, _ := stubEnv(t)
	t.Setenv("OPENARITY_POSTGRES_DSN", "postgres://user:hunter2@localhost:5432/db?sslmode=disable")

	var buf bytes.Buffer
	if err := run(ctx, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("run leaked the Postgres password: %q", buf.String())
	}
}

// main must turn a config error into a non-zero exit code. os.Exit cannot be
// observed in-process, so re-exec the test binary and inspect the real exit.
// The subprocess is the same coverage-instrumented binary and its counters are
// merged, so this is also the only thing covering main. Drop it and main goes
// to 0%.
func TestMainExitsNonZeroOnBadConfig(t *testing.T) {
	if os.Getenv("BRAIN_TEST_SUBPROCESS") == "1" {
		main()
		return
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestMainExitsNonZeroOnBadConfig")
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
