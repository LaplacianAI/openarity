//go:build !linux

package workspace

// Harden does nothing off Linux: a room is a Linux container, and a
// workspace run elsewhere is a test.
func Harden() error { return nil }
