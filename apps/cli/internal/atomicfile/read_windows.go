//go:build windows

package atomicfile

import (
	"io"
	"os"
	"syscall"
)

// Read is os.ReadFile with one difference: the handle it opens lets the
// file be renamed over while it is still being read.
//
// Go opens files for reading with FILE_SHARE_READ and FILE_SHARE_WRITE but not
// FILE_SHARE_DELETE, and Windows refuses to rename over a file any handle has
// open. Every command loads the config file, so `oa config set` failed with
// "Access is denied" whenever another one was running — and Write's whole
// design is a temporary file followed by a rename.
//
// Unix gives this for free: a rename replaces the directory entry, and a
// reader that already has the file keeps reading the one it opened.
func Read(path string) ([]byte, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	handle, err := syscall.CreateFile(name,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		// A PathError carrying the Errno, so errors.Is(err, fs.ErrNotExist)
		// still answers for a config file nobody has written yet. Returning
		// the bare Errno would make Load report a fresh install as a failure.
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	file := os.NewFile(uintptr(handle), path)
	defer func() { _ = file.Close() }()

	return io.ReadAll(file)
}
