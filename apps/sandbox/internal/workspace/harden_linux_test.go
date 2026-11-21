//go:build linux

package workspace

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// Once hardened, the process's environ is no command's to read, though the
// command runs as the same user. Root holds CAP_SYS_PTRACE and reads it
// anyway, which is why a room never runs as root.
func TestAHardenedProcessKeepsItsEnvironFromItsCommands(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any environ; a room runs as nobody")
	}
	if err := Harden(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "cat", "/proc/"+strconv.Itoa(os.Getpid())+"/environ").CombinedOutput()
	if err == nil {
		t.Fatalf("a command read the hardened process's environ: %d bytes", len(out))
	}
}
