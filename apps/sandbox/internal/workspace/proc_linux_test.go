//go:build linux

package workspace

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
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

// The open file is asked, not its path: a file on procfs is known for one
// however it was reached.
func TestAFileOpenedOnProcIsKnownForOne(t *testing.T) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !onProc(f) {
		t.Error("/proc/self/status is not known to be on procfs")
	}
	g, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if onProc(g) {
		t.Error("a temporary file is taken for procfs")
	}
}

// open refuses procfs by itself, not only behind resolve's check of the path.
func TestOpenRefusesProcByTheFile(t *testing.T) {
	f, err := open("/proc/self/environ")
	if err == nil {
		_ = f.Close()
		t.Fatal("open read the process's environ")
	}
	var p *Problem
	if !errors.As(err, &p) || !strings.Contains(p.msg, "/proc") {
		t.Fatalf("open: %v, want it refused for /proc", err)
	}
}
