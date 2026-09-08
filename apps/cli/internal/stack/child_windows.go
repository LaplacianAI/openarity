//go:build windows

package stack

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// The Windows equivalent of a process group. Without this flag the console
// control event below would reach every process attached to the console —
// including the one sending it — so `oa stop` would stop `oa`.
func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Windows has no SIGTERM. CTRL_BREAK is the closest thing a process can
// choose to handle, and Go's runtime delivers it to a child as os.Interrupt —
// so a Go child written to shut down on Interrupt needs no Windows-specific
// code of its own.
//
// Called through kernel32 rather than golang.org/x/sys/windows, which would
// be a new dependency for two lines.
func terminate(p *os.Process) error {
	const ctrlBreakEvent = 1

	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	r, _, err := proc.Call(uintptr(ctrlBreakEvent), uintptr(p.Pid))
	if r == 0 {
		return fmt.Errorf("stack: GenerateConsoleCtrlEvent: %w", err)
	}
	return nil
}

// A child created with an empty environment block fails to start on Windows,
// because the loader needs SystemRoot to find its own DLLs. This is the
// smallest floor that works, and it is not a hole in the isolation: nothing
// under OPENARITY_ passes through it.
func baseEnv() []string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return []string{"SystemRoot=" + root}
	}
	return nil
}
