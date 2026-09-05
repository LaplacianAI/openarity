//go:build !windows

package stack

import (
	"os"
	"os/exec"
	"syscall"
)

// Setpgid puts the child in its own process group, so the signal below
// reaches everything it spawned rather than only the process this package
// started. Postgres is the one that makes this necessary: the postmaster
// forks a writer, a checkpointer and a handful of others, and signalling only
// the parent leaves them running with the data directory still open.
func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate sends SIGTERM to the whole group — the negative PID is what makes
// it the group rather than the one process.
func terminate(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGTERM)
}

// Unix inherits nothing it needs, so the floor is empty and a child gets
// exactly what it was given.
func baseEnv() []string { return nil }
