//go:build linux

package workspace

import (
	"os"
	"syscall"
)

// procSuperMagic is PROC_SUPER_MAGIC, from <linux/magic.h>.
const procSuperMagic = 0x9fa0

// onProc is whether the open file is on procfs. It is asked of the file
// opened, not its path, so a link swapped in between a check of the path
// and the open is still caught.
func onProc(f *os.File) bool {
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil { //nolint:gosec // G115: an fd is small
		return true // unknown is refused
	}
	return st.Type == procSuperMagic
}
