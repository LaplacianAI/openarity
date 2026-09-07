//go:build !windows

package atomicfile

import "os"

// Read is os.ReadFile. Only the Windows build has anything to say about
// sharing: there, a reader blocks the rename Write depends on unless the
// handle it opens allows one. See read_windows.go.
func Read(path string) ([]byte, error) {
	// #nosec G304 -- callers pass a path they built themselves, from a config
	// directory or an install root. Nothing reaches here from a flag or a
	// server.
	return os.ReadFile(path)
}
