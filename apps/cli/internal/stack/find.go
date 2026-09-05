package stack

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Finder locates the binaries the stack supervises. It is an interface with
// one implementation today and a downloading one next: the difference between
// "already on this machine" and "fetch it from a release" is where a binary
// comes from, not what the supervisor does with it.
type Finder interface {
	Find(name string) (string, error)
}

// LocalFinder resolves against the install directory first and PATH second.
type LocalFinder struct {
	// Dir is the install's bin/, or whatever --bin-dir pointed at. Empty
	// means PATH only, which is the state before anything is downloaded.
	Dir string

	// GOOS overrides the platform, so a test on Linux can assert what the
	// finder does on Windows. Empty means this machine.
	GOOS string
}

func (f LocalFinder) Find(name string) (string, error) {
	goos := f.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	// A Windows binary is not called dex.
	filename := name
	if goos == "windows" {
		filename += ".exe"
	}

	// The install directory wins. A machine with its own Postgres on PATH
	// must not have that one supervised by mistake — the installer's copy is
	// the one whose version is in stack.yaml and whose data directory this is.
	if f.Dir != "" {
		path := filepath.Join(f.Dir, filename)
		switch err := runnable(path, goos); {
		case err == nil:
			return path, nil
		case !errors.Is(err, os.ErrNotExist):
			// Present but unusable — a directory, or a file with no
			// executable bit, which is what an interrupted download leaves
			// behind. Falling through to PATH here would silently supervise
			// a different binary than the one on disk.
			return "", fmt.Errorf("stack: %s at %s is not runnable: %w", name, path, err)
		}
	}

	found, err := exec.LookPath(filename)
	if err == nil {
		return found, nil
	}

	// "exec: not found" tells a person nothing they can act on. Which binary
	// is missing, and where it was looked for, tells them exactly what to do.
	if f.Dir != "" {
		return "", fmt.Errorf("stack: no %s in %s or on PATH", name, f.Dir)
	}
	return "", fmt.Errorf("stack: no %s on PATH", name)
}

// runnable reports whether the path is a file this machine could execute.
// Doing it here rather than at exec time means the error names the binary
// instead of arriving several layers away as a permission failure.
func runnable(path, goos string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("it is a directory")
	}

	// Windows decides by extension, which the caller has already applied.
	if goos == "windows" {
		return nil
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("it has no executable bit set")
	}
	return nil
}
