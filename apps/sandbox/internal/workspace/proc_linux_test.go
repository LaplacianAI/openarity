//go:build linux

package workspace

import (
	"io/fs"
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
