//go:build !windows

package stack

import (
	"os/exec"
	"syscall"
)

// detach makes the supervisor a session of its own.
//
// Setsid rather than Setpgid: the supervisor has to survive the process that
// started it, and on macOS that process is a sidecar of the installer window.
// Closing the window would otherwise take the whole install down with it, and
// a terminal's Ctrl-C would reach it too — a person stopping `oa stack setup`
// after it finished would stop Openarity.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
