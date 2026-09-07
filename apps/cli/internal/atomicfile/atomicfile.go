// Package atomicfile replaces a small file in one step, and reads one without
// getting in the way of that replacement.
//
// Three files in this module are written the same way — the config, the
// credential store, and a personal install's state. Each is read far more
// often than it is written, and a half-written one is worse than an old one:
// yaml parses a truncated file happily into a zero value, so a torn read looks
// like a config nobody ever created.
//
// The Unix answer is a temporary file in the same directory and a rename. The
// Windows answer needs one more thing, which is why this is a package rather
// than three copies of six lines. See read_windows.go.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Write puts data at path, creating the directory if it is missing.
//
// A reader sees either the whole of the old file or the whole of the new one,
// never part of either. The temporary file is made in the destination's own
// directory because a rename across filesystems is not atomic — and on Windows
// not even permitted.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// os.CreateTemp creates at 0600, so there is no chmod here. A line that
	// looks like a permission guard and enforces nothing is worse than none,
	// because the next reader stops looking.
	temp, err := os.CreateTemp(dir, tempPattern(path))
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", temp.Name(), err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temp.Name(), err)
	}

	if err := replace(temp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// tempPattern names the temporary file after the one it will become, so a
// leftover says which write was interrupted. config.yaml gives .config-*.yaml.
func tempPattern(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return "." + strings.TrimSuffix(base, ext) + "-*" + ext
}

// Nine attempts over a quarter of a second. Long enough for a read to finish,
// short enough that a rename which will never succeed still reports promptly.
const longestWait = 128 * time.Millisecond

// replace renames over the destination, waiting briefly for a handle held by
// something else to be released.
//
// Read opens files in a way that does not block this, so a reader inside this
// module is not what it waits for. An editor, a backup agent or a virus
// scanner is, and on Windows any of them is enough to make the rename fail.
// On Unix the loop runs once.
func replace(from, to string) error {
	var err error
	for wait := time.Millisecond; ; wait *= 2 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if wait > longestWait {
			return err
		}
		time.Sleep(wait)
	}
}
