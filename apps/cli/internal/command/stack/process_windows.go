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

// interrupt asks the supervisor to stop. CTRL_BREAK is what a Go process
// receives as os.Interrupt, so the supervisor needs no Windows-specific code
// of its own — and it must be an ask rather than a kill, because the
// supervisor's job is stopping the children with pg_ctl first.
func interrupt(proc *os.Process) error {
	const ctrlBreakEvent = 1

	send := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	if r, _, err := send.Call(uintptr(ctrlBreakEvent), uintptr(proc.Pid)); r == 0 {
		return err
	}
	return nil
}

// openBrowser goes through rundll32 rather than `cmd /c start`, whose first
// quoted argument is the window title — so a path or URL containing a space
// silently opens the wrong thing.
func openBrowser(url string) error {
	//nolint:gosec // url is built from a port this process chose
	return exec.CommandContext(context.Background(),
		"rundll32", "url.dll,FileProtocolHandler", url).Start()
}
