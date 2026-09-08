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

	// Looked in before the install's own bin/, and before anything is
	// downloaded. It is where --bin-dir points: a developer with a locally
	// built brain still wants Postgres and MinIO fetched rather than having
	// to build those too.
	Override string
}

func (f DownloadingFinder) Find(ctx context.Context, name string) (string, error) {
	if f.Override != "" {
		override := LocalFinder{Dir: f.Override, GOOS: f.Platform.GOOS, DirOnly: true}
		if path, err := override.Find(ctx, name); err == nil {
			return path, nil
		}
	}

	if path, err := f.installed(ctx, name); err == nil {
		return path, nil
	}

	if err := f.download(ctx, name); err != nil {
		f.Downloader.report(Event{
			Step: StepDownload, Phase: PhaseFailed, Detail: err.Error(),
		})
		return "", err
	}
	return f.installed(ctx, name)
}

// installed looks in bin/, then in the directory Postgres was unpacked into.
//
// Postgres is used where it landed rather than being linked into bin/, because
// its binaries resolve everything else relative to their own location. On
// Windows the 29 DLLs they load sit beside them in the distribution's bin/, so
// an initdb.exe reached through a link in a directory holding none of them
// exits 0xc0000135 — STATUS_DLL_NOT_FOUND — before printing a word. Everything
// downstream derives initdb and pg_ctl from where postgres was found, so this
// is the only place that has to know.
//
// bin/ is searched first, so an install made while the links existed keeps
// working.
func (f DownloadingFinder) installed(ctx context.Context, name string) (string, error) {
	dirs := []string{f.Layout.Bin, filepath.Join(f.Layout.Bin, "postgres-dist", "bin")}

	var err error
	for _, dir := range dirs {
		var path string
		if path, err = (LocalFinder{Dir: dir, GOOS: f.Platform.GOOS, DirOnly: true}).Find(ctx, name); err == nil {
			return path, nil
		}
	}
	return "", err
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
		if err := f.Downloader.Postgres(ctx, url, url+".sha256", dist); err != nil {
			return err
		}
		f.Downloader.report(Event{Step: StepDownload, Phase: PhaseDone, Detail: name})
		return nil
	}

	if name == "minio" {
		url, err := f.Platform.MinIOURL(MinIOVersion)
		if err != nil {
			return err
		}
		if err := f.Downloader.Binary(ctx, url, url+".sha256sum", filepath.Join(f.Layout.Bin, "minio"+suffix(f.Platform))); err != nil {
			return err
		}
		f.Downloader.report(Event{Step: StepDownload, Phase: PhaseDone, Detail: name})
		return nil
	}

	tag := f.Tag
	if tag == "" {
		return fmt.Errorf("stack: %s is not published yet — build it and pass --bin-dir", name)
	}

	release := f.Platform.ReleaseURL(tag, name)
	if err := f.Downloader.Binary(ctx, release, release+".sha256",
		filepath.Join(f.Layout.Bin, name+suffix(f.Platform))); err != nil {
		return err
	}
	f.Downloader.report(Event{Step: StepDownload, Phase: PhaseDone, Detail: name})
	return nil
}

func suffix(p Platform) string {
	if p.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
