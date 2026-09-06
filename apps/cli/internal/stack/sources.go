package stack

import (
	"fmt"
	"runtime"
)

const (
	PostgresVersion = "18.6.0"
	DexVersion      = "v2.45.1"

	mavenBase = "https://repo1.maven.org/maven2/io/zonky/test/postgres"
	dexBase   = "https://github.com/LaplacianAI/openarity/releases/download"
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

func (p Platform) ReleaseURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s_%s_%s%s", dexBase, tag, name, p.GOOS, p.Arch(), p.exe())
}

func (p Platform) exe() string {
	if p.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
