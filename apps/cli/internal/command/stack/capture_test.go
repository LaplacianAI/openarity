package stack

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The bug this is here for. A command that leaves something running behind it
// — which is exactly what pg_ctl does — keeps the write end of a pipe open for
// as long as that something lives. os/exec's Wait does not return until the
// goroutine copying that pipe reaches EOF, so capture waited on a Postgres
// server that had no intention of exiting, and setup stopped at "Creating the
// database" with nothing to say for ten minutes.
//
// Sending the output to a file removes the pipe and the goroutine with it.
func TestCaptureReturnsWhileAChildOutlivesTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh; the Windows install job is what exercises this")
	}
	t.Parallel()

	// `sleep` inherits the command's output and holds it for far longer than
	// the command itself runs.
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "sleep 30 & echo started")

	done := make(chan error, 1)
	go func() { done <- capture(cmd, "sh") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("capture() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture() did not return while a child outlived the command — it is waiting on a pipe again")
	}
}

// The output still has to arrive, or every failure becomes a bare exit status.
func TestCaptureKeepsWhatTheCommandSaid(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "go", "doesnotexist")

	err := capture(cmd, "go")
	if err == nil {
		t.Fatal("capture() of a failing command = nil, want an error")
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("capture() = %q, want it to name the command", err)
	}
	if !strings.Contains(err.Error(), "doesnotexist") {
		t.Errorf("capture() = %q, want it to carry what the command printed", err)
	}
}

func TestCaptureSaysNothingWhenTheCommandSucceeds(t *testing.T) {
	t.Parallel()

	if err := capture(exec.CommandContext(t.Context(), "go", "version"), "go"); err != nil {
		t.Errorf("capture() = %v, want no error", err)
	}
}
