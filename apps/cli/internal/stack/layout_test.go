package stack

import (
	"path/filepath"
	"strings"
	"testing"
)

func lookup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestTheDataDirectoryIsPerPlatform(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "macOS puts application data in Application Support",
			goos: "darwin",
			env:  map[string]string{"HOME": "/Users/x"},
			want: filepath.Join("/Users/x", "Library", "Application Support", "openarity"),
		},
		{
			name: "Linux falls back to the XDG default",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/x"},
			want: filepath.Join("/home/x", ".local", "share", "openarity"),
		},
		{
			name: "XDG_DATA_HOME wins when it is set",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/x", "XDG_DATA_HOME": "/data"},
			want: filepath.Join("/data", "openarity"),
		},
		{
			name: "Windows uses the local profile",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\x\AppData\Local`},
			want: filepath.Join(`C:\Users\x\AppData\Local`, "openarity"),
		},
	} {
		got, err := Dir(tc.goos, lookup(tc.env))
		if err != nil {
			t.Fatalf("%s: Dir() = %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: Dir() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The reason for reading LOCALAPPDATA is invisible in the code that reads it,
// so it is recorded here: a domain-joined Windows machine syncs the Roaming
// profile between logins, and a Postgres cluster copied across the network
// mid-write is a corrupted one. os.UserConfigDir() returns Roaming, which is
// why this package does not use it.
func TestWindowsNeverUsesTheRoamingProfile(t *testing.T) {
	t.Parallel()

	roaming := `C:\Users\x\AppData\Roaming`
	got, err := Dir("windows", lookup(map[string]string{
		"LOCALAPPDATA": `C:\Users\x\AppData\Local`,
		"APPDATA":      roaming,
	}))
	if err != nil {
		t.Fatalf("Dir() = %v", err)
	}
	if strings.Contains(got, "Roaming") {
		t.Errorf("Dir() = %q, which is inside the roaming profile", got)
	}
	if !strings.Contains(got, "Local") {
		t.Errorf("Dir() = %q, want it under the local profile", got)
	}
}

func TestAnUnsetHomeIsAnErrorRatherThanARelativePath(t *testing.T) {
	t.Parallel()

	// A missing variable used to produce filepath.Join("", "…"), which is a
	// relative path — so the install would land in whatever directory the
	// command happened to run from.
	for _, goos := range []string{"darwin", "linux", "windows"} {
		got, err := Dir(goos, lookup(nil))
		if err == nil {
			t.Errorf("Dir(%s) with no environment = %q, want an error", goos, got)
		}
	}
}

func TestEveryPathIsUnderTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	l := NewLayout(root)

	for name, path := range map[string]string{
		"Bin": l.Bin, "Data": l.Data, "Dex": l.Dex, "Logs": l.Logs,
		"State": l.State, "Secret": l.Secret,
	} {
		if !strings.HasPrefix(path, root) {
			t.Errorf("%s = %q, want it under %q", name, path, root)
		}
	}
}

func TestCreateMakesTheDirectoriesAndIsRepeatable(t *testing.T) {
	t.Parallel()

	l := NewLayout(t.TempDir())
	if err := l.Create(); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	// Setup resumes after an interruption, so creating twice is the normal
	// case rather than an error.
	if err := l.Create(); err != nil {
		t.Fatalf("Create() second time = %v", err)
	}
	if l.Installed() {
		t.Error("Installed() is true with no state file — directories alone are a half-finished setup, not an install")
	}
}
