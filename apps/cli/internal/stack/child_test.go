package stack

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The children these tests supervise are this test binary, re-executed with
// helperEnv set. Building a fixture program with `go build` would work too and
// takes seconds per test; re-execution takes milliseconds and runs on every
// platform, which matters because the whole point of this package is that
// Windows behaves differently.
const helperEnv = "STACK_HELPER"

// What a stubborn child prints once it is actually ignoring signals.
const ready = "handler installed"

// TestHelperProcess is not a test. It is the body of every child process the
// tests below start, selected by helperEnv, and it returns immediately when
// that variable is absent so a normal test run ignores it.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		return

	case "stubborn":
		// Catches the graceful signal and does nothing with it — the child
		// that makes Stop's escalation the difference between a command that
		// returns and one that hangs.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt, syscall.SIGTERM)

		// Only now is it stubborn. Announcing it matters: a test that signals
		// before this line kills the child by default handling and passes
		// with the escalation removed, which is exactly what happened the
		// first time this was written.
		if _, err := os.Stdout.WriteString(ready + "\n"); err != nil {
			t.Fatalf("announcing readiness: %v", err)
		}
		time.Sleep(time.Minute)

	case "quick":
		return

	case "noisy":
		if _, err := os.Stdout.WriteString("a line\n"); err != nil {
			t.Fatalf("writing to stdout: %v", err)
		}

	case "environment":
		if _, err := os.Stdout.WriteString(strings.Join(os.Environ(), "\n") + "\n"); err != nil {
			t.Fatalf("writing the environment: %v", err)
		}
	}
}

// helper builds a Child that runs one of the cases above.
func helper(t *testing.T, mode string) *Child {
	t.Helper()

	return &Child{
		Name: mode,
		Path: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess"},
		Env:  []string{helperEnv + "=" + mode},
		Log:  filepath.Join(t.TempDir(), "child.log"),
	}
}

// The test this package exists for. A child that ignores the graceful signal
// must not hang `oa stop` — on a laptop that would mean a person's only way
// out is Activity Monitor, and they would reasonably conclude the tool is
// broken.
func TestStopEscalatesWhenAChildIgnoresTheSignal(t *testing.T) {
	c := helper(t, "stubborn")

	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	waitForReady(t, c)

	done := make(chan error, 1)
	go func() { done <- c.Stop(200 * time.Millisecond) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop() did not return — the escalation to a kill is missing")
	}

	if c.Running() {
		t.Error("Running() is true after Stop returned")
	}
}

func TestStartAndStopAnObedientChild(t *testing.T) {
	c := helper(t, "quick")

	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if c.PID() == 0 {
		t.Error("PID() = 0 after a successful Start")
	}
	if err := c.Stop(2 * time.Second); err != nil {
		t.Errorf("Stop() = %v", err)
	}
}

func TestStoppingSomethingThatIsNotRunningIsNotAnError(t *testing.T) {
	t.Parallel()

	c := helper(t, "quick")
	if err := c.Stop(time.Second); err != nil {
		t.Errorf("Stop() before Start = %v, want nil — stop must be safe to call twice", err)
	}
}

func TestStartingTwiceIsRefused(t *testing.T) {
	c := helper(t, "stubborn")

	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() { _ = c.Stop(time.Second) })
	waitForReady(t, c)

	if err := c.Start(t.Context()); err == nil {
		t.Error("Start() twice = nil, want an error — the second call would orphan the first process")
	}
}

// Go's exec gives a child the parent's entire environment when Env is nil.
// Here that would hand a supervised brain every OPENARITY_* variable in the
// shell that ran `oa start`, silently overriding the config the installer
// wrote — and the symptom would be a brain reaching a database nobody
// configured, with nothing in any log to say why.
func TestAChildGetsOnlyTheEnvironmentItWasGiven(t *testing.T) {
	t.Setenv("OPENARITY_POSTGRES_DSN", "postgres://from-the-parent-shell/db")

	c := helper(t, "environment")
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := c.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}

	logged := read(t, c.Log)
	if strings.Contains(logged, "from-the-parent-shell") {
		t.Error("the child inherited OPENARITY_POSTGRES_DSN from the parent shell")
	}
	if !strings.Contains(logged, helperEnv+"=environment") {
		t.Error("the child did not receive the environment it was given")
	}
}

// A restart must not erase why the last run failed. Truncating here would
// delete the only evidence at exactly the moment somebody goes looking for it.
func TestTheLogIsAppendedToAcrossRestarts(t *testing.T) {
	c := helper(t, "noisy")

	for range 2 {
		if err := c.Start(t.Context()); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		if err := c.Wait(); err != nil {
			t.Fatalf("Wait() = %v", err)
		}
	}

	if got := strings.Count(read(t, c.Log), "a line"); got != 2 {
		t.Errorf("the log holds %d lines, want 2 — the second run truncated the first", got)
	}
}

func TestAMissingBinaryIsReportedByName(t *testing.T) {
	t.Parallel()

	c := &Child{
		Name: "absent",
		Path: filepath.Join(t.TempDir(), "does-not-exist"),
		Log:  filepath.Join(t.TempDir(), "child.log"),
	}

	err := c.Start(t.Context())
	if err == nil {
		t.Fatal("Start() with no binary = nil, want an error")
	}
	// The name, not just the syscall. "fork/exec …: no such file or directory"
	// on its own does not tell a person which of the four processes is missing.
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("Start() = %q, want it to name the child", err)
	}
}

// waitForReady blocks until the child has installed its signal handler. Every
// test that signals a stubborn child has to call this, or it is testing how
// fast a process boots rather than what it does once it has.
func waitForReady(t *testing.T, c *Child) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(c.Log) //nolint:gosec // a path this test created
		if err == nil && strings.Contains(string(raw), ready) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the child never announced that its signal handler was installed")
}

func read(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}
