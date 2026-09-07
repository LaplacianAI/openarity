//go:build windows

package stack

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// alive asks whether a pid is a live process. OpenProcess succeeding is the
// closest Windows equivalent of signal 0, and the handle is closed
// immediately — leaking one would keep the process object alive after it
// exits, so the next check would say a dead pid is running.
func alive(pid int) bool {
	const queryLimitedInformation = 0x1000

	handle, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}

	// STILL_ACTIVE. A process that genuinely exited with 259 is
	// indistinguishable from a running one here, which is a Windows API wart
	// rather than a bug in this: the supervisor exits 0 or 1.
	const stillActive = 259
	return code == stillActive
}

// interrupt asks the supervisor to stop, through the event it waits on.
// stopsignal_windows.go says why this is not a console control event: those
// reach a process group rather than a process, and the group they reached
// included this command, which died of the signal it sent.
func interrupt(root string, _ *os.Process) error {
	return askToStop(root)
}

// openBrowser goes through rundll32 rather than `cmd /c start`, whose first
// quoted argument is the window title — so a path or URL containing a space
// silently opens the wrong thing.
func openBrowser(url string) error {
	//nolint:gosec // url is built from a port this process chose
	return exec.CommandContext(context.Background(),
		"rundll32", "url.dll,FileProtocolHandler", url).Start()
}
