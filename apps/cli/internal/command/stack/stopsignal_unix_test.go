//go:build !windows

package stack

import (
	"testing"
	"time"
)

// Unix has a signal that reaches one process, so there is nothing else to wait
// on and waitForStop hands back a channel that never fires. A nil channel is
// how a select is told to ignore a case; returning a closed one instead would
// make the supervisor exit the moment it started.
func TestWaitForStopNeverFiresOnItsOwn(t *testing.T) {
	t.Parallel()

	stopped, done, err := waitForStop(t.TempDir())
	if err != nil {
		t.Fatalf("waitForStop() = %v", err)
	}
	defer done()

	select {
	case <-stopped:
		t.Fatal("waitForStop() fired with nothing asking, which would stop the supervisor at startup")
	case <-time.After(100 * time.Millisecond):
	}
}
