package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The files that decide which FalkorDB runs and whether it keeps its writes,
// tested as they ship. A copy of their values here would be one more thing to
// keep in step, which is the failure being guarded against.

type service struct {
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment"`
	Env         map[string]string `yaml:"env"`
	Volumes     []string          `yaml:"volumes"`
}

func repoFile(t *testing.T, parts ...string) []byte {
	t.Helper()

	// From apps/brain/internal/graph to the repository root.
	path := filepath.Join(append([]string{"..", "..", "..", ".."}, parts...)...)
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path inside the repository
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return raw
}

func composeFalkor(t *testing.T) service {
	t.Helper()

	var doc struct {
		Services map[string]service `yaml:"services"`
	}
	if err := yaml.Unmarshal(repoFile(t, "deployment", "docker-compose.yml"), &doc); err != nil {
		t.Fatalf("docker-compose.yml: %v", err)
	}
	svc, ok := doc.Services["falkordb"]
	if !ok {
		t.Fatal("docker-compose.yml has no falkordb service")
	}
	return svc
}

type ciJob struct {
	Services map[string]service `yaml:"services"`
	Env      map[string]string  `yaml:"env"`
}

func ciBrain(t *testing.T) ciJob {
	t.Helper()

	var doc struct {
		Jobs map[string]ciJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github", "workflows", "ci.yml"), &doc); err != nil {
		t.Fatalf("ci.yml: %v", err)
	}
	job, ok := doc.Jobs["brain"]
	if !ok {
		t.Fatal("ci.yml has no brain job")
	}
	return job
}

// persists reports whether the server arguments make FalkorDB write every
// change to disk before acknowledging it.
func persists(args string) bool {
	return strings.Contains(args, "--appendonly yes") && strings.Contains(args, "--appendfsync always")
}

// FalkorDB is Redis with a module, and Redis by default persists with
// periodic snapshots. The projector clears an outbox row once the graph has
// acknowledged the write, so a crash before the next snapshot would lose a
// write with nothing left to replay it. Measured on the pinned image, with
// the container killed straight after an acknowledged write:
//
//	defaults                                  the graph is gone
//	--appendonly yes --appendfsync always     the node is there
func TestTheShippedFalkorDBKeepsEveryAcknowledgedWrite(t *testing.T) {
	t.Parallel()

	args := composeFalkor(t).Environment["REDIS_ARGS"]
	if !persists(args) {
		t.Errorf("docker-compose.yml runs FalkorDB with REDIS_ARGS=%q, so a crash loses writes the projector was told succeeded", args)
	}
}

// The server's directory is /var/lib/falkordb/data. A volume mounted
// anywhere else holds nothing, and recreating the container discards the
// graph whatever REDIS_ARGS says.
func TestTheShippedVolumeIsWhereFalkorDBWrites(t *testing.T) {
	t.Parallel()

	for _, v := range composeFalkor(t).Volumes {
		if strings.HasSuffix(v, ":"+falkorDataDir) {
			return
		}
	}
	t.Errorf("docker-compose.yml mounts %v for FalkorDB, and none of them at %s", composeFalkor(t).Volumes, falkorDataDir)
}

const falkorDataDir = "/var/lib/falkordb/data"

// That directory is a fact about the image, not about this repository. It is
// asked of the server under test, so an image that moves it fails here.
func TestTheServerWritesWhereTheVolumeIsMounted(t *testing.T) {
	t.Parallel()

	g, _ := liveGraph(t, 3)
	reply, err := g.db.Conn.Do(context.WithoutCancel(t.Context()), "CONFIG", "GET", "dir").Result()
	if err != nil {
		t.Fatalf("CONFIG GET dir: %v", err)
	}

	var dir string
	switch r := reply.(type) {
	case []any:
		if len(r) == 2 {
			dir, _ = r[1].(string)
		}
	case map[any]any:
		dir, _ = r["dir"].(string)
	}
	if dir != falkorDataDir {
		t.Errorf("FalkorDB writes to %q (reply %v), and compose mounts its volume at %s", dir, reply, falkorDataDir)
	}
}

// A pinned version, and the same one in both places. The graph client leans
// on what this version does with things it could have refused, and testing
// against a real FalkorDB means little on a version nobody deploys.
func TestCITestsTheFalkorDBThatShips(t *testing.T) {
	t.Parallel()

	shipped := composeFalkor(t).Image
	tested, ok := ciBrain(t).Services["falkordb"]
	if !ok {
		t.Fatal("the brain job in ci.yml has no falkordb service, so every graph test skips there")
	}

	if !strings.HasPrefix(shipped, "falkordb/falkordb:v") {
		t.Errorf("docker-compose.yml runs %q, want a pinned falkordb/falkordb:v<version>", shipped)
	}
	if tested.Image != shipped {
		t.Errorf("ci.yml tests against %q and docker-compose.yml ships %q", tested.Image, shipped)
	}
	if !persists(tested.Env["REDIS_ARGS"]) {
		t.Errorf("ci.yml runs FalkorDB with REDIS_ARGS=%q, not as it ships", tested.Env["REDIS_ARGS"])
	}
}

// Without the variable every test in this package skips, and a skip passes.
// CI would stay green with the graph untested.
func TestCISetsTheVariableTheGraphTestsNeed(t *testing.T) {
	t.Parallel()

	if url := ciBrain(t).Env["BRAIN_TEST_FALKOR_URL"]; !strings.HasPrefix(url, "redis://") {
		t.Errorf("the brain job sets BRAIN_TEST_FALKOR_URL=%q, so the graph tests skip in CI", url)
	}
}
