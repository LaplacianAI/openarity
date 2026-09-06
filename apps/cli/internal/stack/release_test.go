package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const workflow = "../../../../.github/workflows/release.yml"

type releaseWorkflow struct {
	Jobs struct {
		Build struct {
			Strategy struct {
				Matrix struct {
					Include []struct {
						GOOS   string `yaml:"goos"`
						GOARCH string `yaml:"goarch"`
					} `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"build"`
	} `yaml:"jobs"`
}

func matrix(t *testing.T) []Platform {
	t.Helper()

	raw, err := os.ReadFile(filepath.FromSlash(workflow)) //nolint:gosec // a path in this repository
	if err != nil {
		t.Fatalf("reading the release workflow: %v", err)
	}

	var w releaseWorkflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatalf("parsing the release workflow: %v", err)
	}

	out := make([]Platform, 0, len(w.Jobs.Build.Strategy.Matrix.Include))
	for _, e := range w.Jobs.Build.Strategy.Matrix.Include {
		out = append(out, Platform{GOOS: e.GOOS, GOARCH: e.GOARCH})
	}
	if len(out) == 0 {
		t.Fatal("the release workflow declares no build targets")
	}
	return out
}

// Building a binary for a platform with no Postgres artifacts would produce an
// oa that installs nothing — the download would 404 on the one thing it cannot
// build itself.
func TestEveryReleasedPlatformCanAlsoGetPostgres(t *testing.T) {
	t.Parallel()

	for _, p := range matrix(t) {
		if _, err := p.PostgresURL(PostgresVersion); err != nil {
			t.Errorf("the release builds %s/%s, but %v", p.GOOS, p.GOARCH, err)
		}
	}
}

// The other direction, which is the one that rots: adding a platform to
// sources.go and forgetting the workflow leaves oa asking for an asset that
// was never built, and the failure appears at a stranger's first install
// rather than in CI.
func TestEverySupportedPlatformIsBuilt(t *testing.T) {
	t.Parallel()

	built := map[Platform]bool{}
	for _, p := range matrix(t) {
		built[p] = true
	}

	for _, p := range []Platform{
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "amd64"},
	} {
		if !built[p] {
			t.Errorf("%s/%s is supported but the release does not build it", p.GOOS, p.GOARCH)
		}
	}

	// windows/arm64 runs the amd64 build under emulation, so building a
	// native one would be an asset nothing ever asks for.
	if built[Platform{GOOS: "windows", GOARCH: "arm64"}] {
		t.Error("the release builds windows/arm64, which no install downloads")
	}
}

// The workflow greps the pinned dex version out of sources.go so the two
// cannot disagree. If that expression stops matching, the grep returns empty
// and the build clones a branch called "".
func TestTheWorkflowCanFindThePinnedDexVersion(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.FromSlash(workflow)) //nolint:gosec // a path in this repository
	if err != nil {
		t.Fatalf("reading the release workflow: %v", err)
	}
	if !strings.Contains(string(raw), "DexVersion") {
		t.Fatal("the release workflow does not read DexVersion from sources.go")
	}

	source, err := os.ReadFile("sources.go")
	if err != nil {
		t.Fatalf("reading sources.go: %v", err)
	}
	if !strings.Contains(string(source), `DexVersion      = "`+DexVersion+`"`) {
		t.Errorf("DexVersion is %q, but sources.go does not declare it in the shape the workflow greps for", DexVersion)
	}
}

// Each artifact carries its own .sha256 because the downloader fetches
// <url>.sha256 per file. A combined checksums.txt would mean parsing a
// manifest to verify one binary, and the downloader does not.
func TestTheWorkflowPublishesPerArtifactChecksums(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.FromSlash(workflow)) //nolint:gosec // a path in this repository
	if err != nil {
		t.Fatalf("reading the release workflow: %v", err)
	}
	if !strings.Contains(string(raw), "$f.sha256") {
		t.Error("the release workflow does not write a .sha256 beside each artifact")
	}
}
