//go:build !linux

package workspace

import "os"

// onProc is false off Linux, which has no procfs to guard.
func onProc(*os.File) bool { return false }
