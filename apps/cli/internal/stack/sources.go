package stack

import (
	"fmt"
	"runtime"
)

const (
	PostgresVersion = "18.6.0"
	DexVersion      = "v2.45.1"
	MinIOVersion    = "RELEASE.2025-09-07T16-13-09Z"

	// The runtimes a model gateway needs, downloaded for the same reason
	// Postgres is: a personal install may not assume anything is already on
	// the machine. LiteLLM publishes no binary at all and OmniRoute publishes
	// a desktop application, so the runtime is fetched and the gateway
	// installed into it.
	//
	// uv rather than a Python, because it is one static binary that
	// provisions its own interpreter — no system Python to find, match or
	// apologise for.
	UvVersion   = "0.12.10"
	NodeVersion = "v24.20.0"

	// The gateways, pinned like everything else. LiteLLM's proxy extra is
	// what serves an OpenAI-compatible endpoint; the bare package is a
	// library.
	LiteLLMVersion   = "1.100.0"
	OmniRouteVersion = "3.8.50"

	mavenBase = "https://repo1.maven.org/maven2/io/zonky/test/postgres"
	dexBase   = "https://github.com/LaplacianAI/openarity/releases/download"
	minioBase = "https://dl.min.io/server/minio/release"
	uvBase    = "https://github.com/astral-sh/uv/releases/download"
	nodeBase  = "https://nodejs.org/dist"
)

type Platform struct {
	GOOS   string
	GOARCH string
}

func ThisPlatform() Platform {
	return Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

func (p Platform) Emulated() bool {
	return p.GOOS == "windows" && p.GOARCH == "arm64"
}

func (p Platform) Arch() string {
	if p.Emulated() {
		return "amd64"
	}
	return p.GOARCH
}

func (p Platform) zonky() (string, error) {
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64v8"}[p.Arch()]
	if arch == "" {
		return "", fmt.Errorf("stack: no Postgres binaries for %s/%s", p.GOOS, p.GOARCH)
	}

	switch p.GOOS {
	case "darwin", "linux", "windows":
		if p.GOOS == "windows" && arch != "amd64" {
			return "", fmt.Errorf("stack: no Postgres binaries for %s/%s", p.GOOS, p.GOARCH)
		}
		return p.GOOS + "-" + arch, nil
	default:
		return "", fmt.Errorf("stack: no Postgres binaries for %s/%s", p.GOOS, p.GOARCH)
	}
}

func (p Platform) PostgresURL(version string) (string, error) {
	name, err := p.zonky()
	if err != nil {
		return "", err
	}

	artifact := "embedded-postgres-binaries-" + name
	return fmt.Sprintf("%s/%s/%s/%s-%s.jar", mavenBase, artifact, version, artifact, version), nil
}

// MinIOURL names the pinned artifact rather than the floating alias beside
// it. Every platform uses the same file name in the archive directory —
// including Windows, whose current-release download is minio.exe but whose
// archived one is not.
func (p Platform) MinIOURL(version string) (string, error) {
	switch p.GOOS {
	case "darwin", "linux", "windows":
	default:
		return "", fmt.Errorf("stack: no MinIO build for %s/%s", p.GOOS, p.GOARCH)
	}
	return fmt.Sprintf("%s/%s-%s/archive/minio.%s", minioBase, p.GOOS, p.Arch(), version), nil
}

// uvTriple is Rust's target naming, which is what uv publishes under.
func (p Platform) uvTriple() (string, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[p.Arch()]
	if arch == "" {
		return "", fmt.Errorf("stack: no uv build for %s/%s", p.GOOS, p.GOARCH)
	}

	switch p.GOOS {
	case "darwin":
		return arch + "-apple-darwin", nil
	case "linux":
		return arch + "-unknown-linux-gnu", nil
	case "windows":
		return "x86_64-pc-windows-msvc", nil
	default:
		return "", fmt.Errorf("stack: no uv build for %s/%s", p.GOOS, p.GOARCH)
	}
}

// UvURL names the archive holding one binary. Windows ships a zip and
// everything else a gzipped tar, so the extension is part of what this
// returns rather than something the caller assumes.
func (p Platform) UvURL(version string) (string, error) {
	triple, err := p.uvTriple()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/uv-%s%s", uvBase, version, triple, p.archiveExt()), nil
}

// nodeName is Node's own naming, which agrees with neither Go's nor Rust's:
// x64 rather than amd64 or x86_64, and win rather than windows.
func (p Platform) nodeName() (string, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[p.Arch()]
	if arch == "" {
		return "", fmt.Errorf("stack: no Node build for %s/%s", p.GOOS, p.GOARCH)
	}

	switch p.GOOS {
	case "darwin", "linux":
		return p.GOOS + "-" + arch, nil
	case "windows":
		return "win-x64", nil
	default:
		return "", fmt.Errorf("stack: no Node build for %s/%s", p.GOOS, p.GOARCH)
	}
}

func (p Platform) NodeURL(version string) (string, error) {
	name, err := p.NodeArchiveName(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s", nodeBase, version, name), nil
}

// NodeChecksumURL is one file covering every build of a release, so the
// downloader is told which line to read rather than taking the first.
func (p Platform) NodeChecksumURL(version string) string {
	return fmt.Sprintf("%s/%s/SHASUMS256.txt", nodeBase, version)
}

// NodeArchiveName is the line to find in that file, and the file to fetch.
func (p Platform) NodeArchiveName(version string) (string, error) {
	name, err := p.nodeName()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("node-%s-%s%s", version, name, p.archiveExt()), nil
}

func (p Platform) archiveExt() string {
	if p.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func (p Platform) ReleaseURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s_%s_%s%s", dexBase, tag, name, p.GOOS, p.Arch(), p.exe())
}

func (p Platform) exe() string {
	if p.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
