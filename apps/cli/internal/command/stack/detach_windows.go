//go:build windows

package stack

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS gives the supervisor no console at all, which is what
// stops a Ctrl-C in the console that started it reaching the whole install.
// CREATE_NEW_PROCESS_GROUP for the same reason it is set on every other child
// here: a console control event is delivered to a group, never to a pid.
const detachedProcess = 0x00000008

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
