package stack

import (
	"context"
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
	Find(ctx context.Context, name string) (string, error)
}

// LocalFinder resolves against the install directory first and PATH second.
type LocalFinder struct {
	// Dir is the install's bin/, or whatever --bin-dir pointed at. Empty
	// means PATH only, which is the state before anything is downloaded.
	Dir string

	// GOOS overrides the platform, so a test on Linux can assert what the
	// finder does on Windows. Empty means this machine.
	GOOS string

	DirOnly bool
}

func (f LocalFinder) Find(_ context.Context, name string) (string, error) {
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

	if !f.DirOnly {
		if found, err := exec.LookPath(filename); err == nil {
			return found, nil
		}
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

type DownloadingFinder struct {
	Layout     Layout
	Platform   Platform
	Downloader *Downloader
	Tag        string
}

func (f DownloadingFinder) Find(ctx context.Context, name string) (string, error) {
	local := LocalFinder{Dir: f.Layout.Bin, GOOS: f.Platform.GOOS, DirOnly: true}

	if path, err := local.Find(ctx, name); err == nil {
		return path, nil
	}

	if err := f.download(ctx, name); err != nil {
		f.Downloader.report(Event{
			Step: StepDownload, Phase: PhaseFailed, Detail: err.Error(),
		})
		return "", err
	}
	return local.Find(ctx, name)
}

func (f DownloadingFinder) download(ctx context.Context, name string) error {
	f.Downloader.report(Event{
		Step: StepDownload, Phase: PhaseStarted, Detail: name,
	})

	if name == "postgres" {
		url, err := f.Platform.PostgresURL(PostgresVersion)
		if err != nil {
			return err
		}

		dist := filepath.Join(f.Layout.Bin, "postgres-dist")
		if err := f.Downloader.Postgres(ctx, url, dist); err != nil {
			return err
		}
		if err := linkPostgres(dist, f.Layout.Bin, f.Platform); err != nil {
			return err
		}
		f.Downloader.report(Event{Step: StepDownload, Phase: PhaseDone, Detail: name})
		return nil
	}

	tag := f.Tag
	if tag == "" {
		return fmt.Errorf("stack: %s is not published yet — build it and pass --bin-dir", name)
	}

	if err := f.Downloader.Binary(ctx, f.Platform.ReleaseURL(tag, name),
		filepath.Join(f.Layout.Bin, name+suffix(f.Platform))); err != nil {
		return err
	}
	f.Downloader.report(Event{Step: StepDownload, Phase: PhaseDone, Detail: name})
	return nil
}

func linkPostgres(dist, bin string, platform Platform) error {
	for _, name := range []string{"postgres", "initdb", "pg_ctl"} {
		target := filepath.Join(dist, "bin", name+suffix(platform))
		link := filepath.Join(bin, name+suffix(platform))

		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	return nil
}

func suffix(p Platform) string {
	if p.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
