//go:build !windows

package config

import "os"

// readFile is os.ReadFile. Only the Windows build needs to say anything about
// sharing: there, a reader blocks the rename Save depends on unless the handle
// allows it. See read_windows.go.
func readFile(path string) ([]byte, error) {
	// #nosec G304 -- the path is built by Path() from the user's own config
	// directory, not from input. Nothing reaches it from a flag or a server.
	return os.ReadFile(path)
}
