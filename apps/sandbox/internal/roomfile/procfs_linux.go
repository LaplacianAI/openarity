//go:build linux

package roomfile

import (
	"os"
	"syscall"
)

// procSuperMagic is PROC_SUPER_MAGIC, from <linux/magic.h>.
const procSuperMagic = 0x9fa0

// OnProc is whether the open file is on procfs. It is asked of the file
// opened, not its path, so a link swapped in between a check of the path
// and the open is still caught.
func OnProc(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return true // unknown is refused
	}
	var st syscall.Statfs_t
	var serr error
	// Through the raw conn, not Fd, which would set the file blocking.
	if err := rc.Control(func(fd uintptr) { serr = syscall.Fstatfs(int(fd), &st) }); err != nil || serr != nil { //nolint:gosec // G115: an fd is small
		return true
	}
	return st.Type == procSuperMagic
}
