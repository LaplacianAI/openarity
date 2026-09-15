//go:build windows

package stack

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point: setting the event has to reach the thing waiting on it, and
// nothing else. A console control event reached a process group instead, which
// is how `oa stack stop` came to kill itself with exit 0xC000013A.
func TestStopReachesTheSupervisorWaitingOnIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	stopped, done, err := waitForStop(root)
	if err != nil {
		t.Fatalf("waitForStop() = %v", err)
	}
	defer done()

	select {
	case <-stopped:
		t.Fatal("the supervisor was told to stop before anything asked")
	case <-time.After(50 * time.Millisecond):
	}

	if err := askToStop(root); err != nil {
		t.Fatalf("askToStop() = %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("askToStop() did not reach the supervisor waiting on it")
	}
}

// Two installs on one machine, and stopping one must not stop the other. The
// name carries the root for exactly this.
func TestStoppingOneInstallLeavesAnotherRunning(t *testing.T) {
	t.Parallel()

	first, second := t.TempDir(), t.TempDir()

	stopped, done, err := waitForStop(second)
	if err != nil {
		t.Fatalf("waitForStop() = %v", err)
	}
	defer done()

	if err := askToStop(first); err == nil {
		t.Error("askToStop() on an install with no supervisor succeeded, so it reached something else")
	}

	select {
	case <-stopped:
		t.Fatal("stopping one install stopped another")
	case <-time.After(100 * time.Millisecond):
	}
}

// Asking to stop what is not running is a sentence, not a crash. It is what
// happens when somebody runs `oa stack stop` twice.
func TestStoppingWhatIsNotRunningIsAnError(t *testing.T) {
	t.Parallel()

	err := askToStop(t.TempDir())
	if err == nil {
		t.Fatal("askToStop() with no supervisor = nil, want an error")
	}
	if !strings.Contains(err.Error(), "nothing is waiting") {
		t.Errorf("askToStop() = %q, want it to say nothing is running", err)
	}
}

// An event name may not contain a backslash and is capped at MAX_PATH, which
// an install root under a deep profile directory would breach on its own.
func TestTheEventNameSurvivesAnyRoot(t *testing.T) {
	t.Parallel()

	deep := filepath.Join(`C:\Users\somebody\AppData\Local`, strings.Repeat("a-long-directory-name", 40))

	name := stopEventName(deep)
	if strings.Count(name, `\`) != 1 {
		t.Errorf("name = %q, want exactly the one backslash in the Local\\ prefix", name)
	}
	if len(name) > 200 {
		t.Errorf("name is %d characters, want it well inside MAX_PATH", len(name))
	}
	if !strings.HasPrefix(name, `Local\`) {
		t.Errorf("name = %q, want it per-session so two people do not collide", name)
	}
	if stopEventName(deep) != name {
		t.Error("the name is not stable, so stop could never find start")
	}
}

// Windows paths are case-insensitive, so the same install spelled two ways is
// the same install.
func TestTheEventNameIgnoresCase(t *testing.T) {
	t.Parallel()

	if stopEventName(`C:\Users\Somebody\openarity`) != stopEventName(`c:\users\somebody\OPENARITY`) {
		t.Error("the same root spelled differently produced two events, so stop would miss start")
	}
}
