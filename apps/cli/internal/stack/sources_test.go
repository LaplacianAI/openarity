package stack

import (
	"strings"
	"testing"
)

func TestEverySupportedPlatformHasPostgresBinaries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "darwin-arm64v8"},
		{"darwin", "amd64", "darwin-amd64"},
		{"linux", "arm64", "linux-arm64v8"},
		{"linux", "amd64", "linux-amd64"},
		{"windows", "amd64", "windows-amd64"},
		{"windows", "arm64", "windows-amd64"},
	} {
		url, err := Platform{GOOS: tc.goos, GOARCH: tc.goarch}.PostgresURL(PostgresVersion)
		if err != nil {
			t.Errorf("%s/%s: %v", tc.goos, tc.goarch, err)
			continue
		}
		if !strings.Contains(url, tc.want) {
			t.Errorf("%s/%s gave %q, want it to name %q", tc.goos, tc.goarch, url, tc.want)
		}
		if !strings.HasSuffix(url, ".jar") {
			t.Errorf("%s/%s gave %q, want a jar", tc.goos, tc.goarch, url)
		}
	}
}

func TestWindowsOnArmIsReportedAsEmulated(t *testing.T) {
	t.Parallel()

	p := Platform{GOOS: "windows", GOARCH: "arm64"}
	if !p.Emulated() {
		t.Error("windows/arm64 is not reported as emulated, but no native binaries are published for it")
	}
	if p.Arch() != "amd64" {
		t.Errorf("Arch() = %q, want amd64 — the only build that exists", p.Arch())
	}

	native := Platform{GOOS: "darwin", GOARCH: "arm64"}
	if native.Emulated() {
		t.Error("darwin/arm64 is reported as emulated, but zonky publishes darwin-arm64v8")
	}
}

func TestAnUnsupportedPlatformSaysSoRatherThanGuessing(t *testing.T) {
	t.Parallel()

	for _, p := range []Platform{
		{GOOS: "freebsd", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "riscv64"},
	} {
		if url, err := p.PostgresURL(PostgresVersion); err == nil {
			t.Errorf("%s/%s produced %q, want an error", p.GOOS, p.GOARCH, url)
		}
	}
}

func TestAReleaseURLNamesThePlatformAndTheTag(t *testing.T) {
	t.Parallel()

	got := Platform{GOOS: "windows", GOARCH: "arm64"}.ReleaseURL("v0.2.0", "dex")
	for _, want := range []string{"v0.2.0", "dex_windows_amd64.exe"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReleaseURL() = %q, want it to contain %q", got, want)
		}
	}
}

// Every platform uses the same archived file name, including Windows — whose
// current-release download is minio.exe but whose archived one is not. Getting
// that wrong is a 404 on one platform only.
func TestMinIOHasOneArtifactNameEverywhere(t *testing.T) {
	t.Parallel()

	for _, p := range []Platform{
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "amd64"},
	} {
		url, err := p.MinIOURL(MinIOVersion)
		if err != nil {
			t.Errorf("%s/%s: %v", p.GOOS, p.GOARCH, err)
			continue
		}
		if !strings.HasSuffix(url, "/archive/minio."+MinIOVersion) {
			t.Errorf("%s/%s gave %q, want the archived name", p.GOOS, p.GOARCH, url)
		}
		if strings.Contains(url, ".exe") {
			t.Errorf("%s/%s gave %q — the archived artifact carries no .exe", p.GOOS, p.GOARCH, url)
		}
	}
}

// Pinned like Postgres and dex. The alias beside it moves, and an install that
// tracked it would record a version it did not download.
func TestTheMinIOVersionIsPinned(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(MinIOVersion, "RELEASE.") {
		t.Errorf("MinIOVersion = %q, want a pinned RELEASE stamp", MinIOVersion)
	}
}
