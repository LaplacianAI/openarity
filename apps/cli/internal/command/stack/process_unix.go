//go:build !windows

package stack

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// alive asks whether a pid is a live process. Signal 0 performs the
// permission and existence checks and delivers nothing, which is the only way
// to ask without also doing something.
func alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// interrupt asks the supervisor to stop. SIGTERM rather than SIGKILL: the
// supervisor's whole job is stopping the children in reverse order with
// pg_ctl, and killing it outright skips all of that and leaves four processes
// behind holding the data directory.
func interrupt(_ string, proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}

// openBrowser is best-effort by design — a headless machine has none, and the
// address is printed either way.
func openBrowser(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}

	//nolint:gosec // url is built from a port this process chose
	return exec.CommandContext(context.Background(), opener, url).Start()
}
