package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func executable(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// hostBinary is what the finder will look for on the machine running the test.
// Writing a file called "postgres" and expecting Find to return it asserts
// Unix rather than the finder: on Windows it looks for postgres.exe, and every
// one of these tests failed there for that reason and no other.
func hostBinary(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestABinaryInTheInstallDirectoryIsFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	want := executable(t, dir, hostBinary("dex"))

	got, err := LocalFinder{Dir: dir}.Find(t.Context(), "dex")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if got != want {
		t.Errorf("Find() = %q, want %q", got, want)
	}
}

// The install directory wins over PATH. A machine with its own Postgres on
// PATH must not have that one supervised by mistake: the installer's copy is
// the one whose version is recorded in stack.yaml and whose data directory
// this is.
func TestTheInstallDirectoryIsPreferredOverPath(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()

	want := executable(t, dir, hostBinary("postgres"))
	executable(t, elsewhere, hostBinary("postgres"))
	t.Setenv("PATH", elsewhere)

	got, err := LocalFinder{Dir: dir}.Find(t.Context(), "postgres")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if got != want {
		t.Errorf("Find() = %q, want the install directory's copy at %q", got, want)
	}
}

// With no install directory yet — the case before anything is downloaded —
// PATH is the fallback, so a developer can point the installer at binaries
// they already have.
func TestPathIsTheFallbackWhenThereIsNoInstallDirectory(t *testing.T) {
	elsewhere := t.TempDir()
	want := executable(t, elsewhere, hostBinary("brain"))
	t.Setenv("PATH", elsewhere)

	got, err := LocalFinder{}.Find(t.Context(), "brain")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if got != want {
		t.Errorf("Find() = %q, want %q", got, want)
	}
}

func TestWindowsLooksForAnExeSuffix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	want := executable(t, dir, "dex.exe")

	got, err := LocalFinder{Dir: dir, GOOS: "windows"}.Find(t.Context(), "dex")
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if got != want {
		t.Errorf("Find() = %q, want %q — a Windows binary is not called dex", got, want)
	}
}

// The message is the feature. "exec: not found" tells a person nothing they
// can act on; which binary is missing and where it was looked for tells them
// exactly what to do next.
func TestAMissingBinarySaysWhichOneAndWhereItLooked(t *testing.T) {
	dir := t.TempDir()

	// PATH is emptied deliberately. Without this the test passes or fails
	// depending on whether the machine running it happens to have Postgres
	// installed — which it did, so this asserted the machine rather than the
	// message the first time it was written.
	t.Setenv("PATH", t.TempDir())

	_, err := LocalFinder{Dir: dir}.Find(t.Context(), "postgres")
	if err == nil {
		t.Fatal("Find() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("Find() = %q, want it to name the binary", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("Find() = %q, want it to name the directory it searched", err)
	}
}

// A file that is not executable fails at exec time with an error about a
// permission, several layers away from the thing that is actually wrong. It
// is a plausible state too: an interrupted download leaves a file behind.
func TestAFileThatIsNotExecutableIsNotABinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit is not how Windows decides")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, hostBinary("dex"))
	if err := os.WriteFile(path, []byte("not a program"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	if _, err := (LocalFinder{Dir: dir}).Find(t.Context(), "dex"); err == nil {
		t.Error("Find() found a file with no executable bit set")
	}
}

// The case the fallthrough would get wrong, and the reason there is no
// fallthrough: an unusable binary in the install directory with a working one
// on PATH. Falling back would supervise a different Postgres than the one in
// bin/, against a data directory the other one initialised, with the version
// in stack.yaml naming neither. An interrupted download leaves exactly this
// state, so it is not hypothetical.
func TestAnUnusableInstallCopyIsAnErrorRatherThanAFallbackToPath(t *testing.T) {
	// The state this is about cannot exist on Windows: there is no executable
	// bit, so a half-written postgres.exe is as runnable as a real one as far
	// as anything here can tell. TestADirectoryIsNotABinary covers the
	// unusable case that does exist there.
	if runtime.GOOS == "windows" {
		t.Skip("a file with no executable bit is not a state Windows has")
	}

	dir := t.TempDir()
	elsewhere := t.TempDir()

	broken := filepath.Join(dir, hostBinary("postgres"))
	if err := os.WriteFile(broken, []byte("half a download"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", broken, err)
	}
	working := executable(t, elsewhere, hostBinary("postgres"))
	t.Setenv("PATH", elsewhere)

	got, err := LocalFinder{Dir: dir}.Find(t.Context(), "postgres")
	if err == nil {
		t.Fatalf("Find() = %q with no error, want a refusal — that is the copy on PATH, not the install's", got)
	}
	if strings.Contains(err.Error(), working) {
		t.Errorf("Find() = %q, want it to name the unusable copy rather than the one on PATH", err)
	}
	if !strings.Contains(err.Error(), broken) {
		t.Errorf("Find() = %q, want it to name %q", err, broken)
	}
}

// The finder that downloads must never resolve from PATH. A machine with its
// own Postgres would otherwise have that one supervised in place of the
// pinned version — against a data directory initialised by a different build,
// with stack.yaml recording a version that is not what is running. It was
// doing exactly this the first time it ran.
func TestTheDownloadingFinderDoesNotFallBackToPath(t *testing.T) {
	elsewhere := t.TempDir()
	executable(t, elsewhere, hostBinary("postgres"))
	t.Setenv("PATH", elsewhere)

	if _, err := (LocalFinder{Dir: t.TempDir(), DirOnly: true}).Find(t.Context(), "postgres"); err == nil {
		t.Error("a DirOnly finder resolved a binary from PATH")
	}
}

// --bin-dir means "look here first", not "only here". A developer with a
// locally built brain still wants Postgres and MinIO downloaded rather than
// having to produce those as well.
func TestAnOverrideIsPreferredButNotExclusive(t *testing.T) {
	t.Parallel()

	override := t.TempDir()
	want := executable(t, override, hostBinary("brain"))

	f := DownloadingFinder{
		Layout:     NewLayout(t.TempDir()),
		Platform:   ThisPlatform(),
		Downloader: &Downloader{},
		Override:   override,
	}

	got, err := f.Find(t.Context(), "brain")
	if err != nil {
		t.Fatalf("Find(brain) = %v", err)
	}
	if got != want {
		t.Errorf("Find(brain) = %q, want the override at %q", got, want)
	}
}

func TestADirectoryIsNotABinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, hostBinary("brain")), 0o700); err != nil {
		t.Fatalf("making a directory: %v", err)
	}

	if _, err := (LocalFinder{Dir: dir}).Find(t.Context(), "brain"); err == nil {
		t.Error("Find() returned a directory as a binary")
	}
}

// Postgres is used where it was unpacked. Its binaries resolve share/ and, on
// Windows, the 29 DLLs beside them relative to their own directory, so moving
// or linking the three executables into bin/ leaves initdb exiting
// 0xc0000135 with nothing written to any log.
func TestPostgresIsFoundWhereItWasUnpacked(t *testing.T) {
	t.Parallel()

	layout := NewLayout(t.TempDir())
	if err := layout.Create(); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	dist := filepath.Join(layout.Bin, "postgres-dist", "bin")
	if err := os.MkdirAll(dist, 0o700); err != nil {
		t.Fatalf("making %s: %v", dist, err)
	}
	want := executable(t, dist, hostBinary("postgres"))

	f := DownloadingFinder{Layout: layout, Platform: ThisPlatform(), Downloader: &Downloader{}}
	got, err := f.Find(t.Context(), "postgres")
	if err != nil {
		t.Fatalf("Find(postgres) = %v", err)
	}
	if got != want {
		t.Errorf("Find(postgres) = %q, want the copy in the distribution at %q", got, want)
	}
}

// bin/ is searched first, so an install made while the binaries were linked
// there goes on working rather than downloading 71MB again.
func TestABinaryDirectlyInBinIsStillPreferred(t *testing.T) {
	t.Parallel()

	layout := NewLayout(t.TempDir())
	if err := layout.Create(); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	want := executable(t, layout.Bin, hostBinary("postgres"))

	dist := filepath.Join(layout.Bin, "postgres-dist", "bin")
	if err := os.MkdirAll(dist, 0o700); err != nil {
		t.Fatalf("making %s: %v", dist, err)
	}
	executable(t, dist, hostBinary("postgres"))

	f := DownloadingFinder{Layout: layout, Platform: ThisPlatform(), Downloader: &Downloader{}}
	got, err := f.Find(t.Context(), "postgres")
	if err != nil {
		t.Fatalf("Find(postgres) = %v", err)
	}
	if got != want {
		t.Errorf("Find(postgres) = %q, want bin/ to win at %q", got, want)
	}
}
