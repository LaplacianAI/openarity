//go:build linux

package workspace

import "syscall"

// prSetDumpable is PR_SET_DUMPABLE, from <linux/prctl.h>.
const prSetDumpable = 4

// Harden makes this process undumpable. The kernel then refuses its
// /proc/<pid>/environ and /proc/<pid>/mem to every process without
// CAP_SYS_PTRACE, the commands it runs as the same user among them, and the
// token the room was started with stays in that environ.
func Harden() error {
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
		return e
	}
	return nil
}
