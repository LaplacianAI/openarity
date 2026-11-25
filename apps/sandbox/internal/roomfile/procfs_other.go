//go:build !linux

package roomfile

import "os"

// OnProc is false off Linux, which has no procfs to guard.
func OnProc(*os.File) bool { return false }
