//go:build linux

package workspace

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/sandbox/internal/roomfile"
)

// A search from / passes /proc by: Glob and Grep run in the workspace
// process, and /proc/self/environ there holds the token.
func TestASearchFromTheRootPassesProcBy(t *testing.T) {
	var inProc []string
	err := walk("/", func(p, _ string, _ fs.DirEntry) error {
		if p > "/proc/~" {
			return fs.SkipAll // past where /proc would be
		}
		if strings.HasPrefix(p, "/proc/") {
			inProc = append(inProc, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inProc) != 0 {
		t.Fatalf("the walk read %d files under /proc, such as %s", len(inProc), inProc[0])
	}
}

// open refuses procfs by itself, not only behind resolve's check of the path.
func TestOpenRefusesProcByTheFile(t *testing.T) {
	f, err := roomfile.Open("/proc/self/environ")
	if err == nil {
		_ = f.Close()
		t.Fatal("open read the process's environ")
	}
	var p *Problem
	if !errors.As(err, &p) || !strings.Contains(p.Error(), "/proc") {
		t.Fatalf("open: %v, want it refused for /proc", err)
	}
}
