//go:build linux

package roomfile

import (
	"os"
	"testing"
)

// The open file is asked, not its path: a file on procfs is known for one
// however it was reached.
func TestAFileOpenedOnProcIsKnownForOne(t *testing.T) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !OnProc(f) {
		t.Error("/proc/self/status is not known to be on procfs")
	}
	g, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if OnProc(g) {
		t.Error("a temporary file is taken for procfs")
	}
}
