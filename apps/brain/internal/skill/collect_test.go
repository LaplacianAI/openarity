package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// endless is a sender that never stops: every read fills the buffer. It counts
// what was taken, which is the only honest measure of what a limit cost.
type endless struct{ taken int }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	e.taken += len(p)
	return len(p), nil
}

// failing hands over some bytes, then breaks, as a dropped connection does.
type failing struct{ sent bool }

func (f *failing) Read(p []byte) (int, error) {
	if f.sent {
		return 0, errors.New("connection reset")
	}
	f.sent = true
	return copy(p, "partial"), nil
}

func mustAdd(t *testing.T, c *Collector, path string, data []byte) {
	t.Helper()

	if err := c.Add(path, bytes.NewReader(data)); err != nil {
		t.Fatalf("Add(%q): %v", path, err)
	}
}

func addRefused(t *testing.T, c *Collector, path string, r io.Reader, want string) {
	t.Helper()

	err := c.Add(path, r)
	if err == nil {
		t.Fatal("accepted")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to say %q", err, want)
	}
}

func TestTheCollectorKeepsEveryFileInTheOrderItCame(t *testing.T) {
	t.Parallel()

	var c Collector
	mustAdd(t, &c, "SKILL.md", []byte(pdfManifest))
	mustAdd(t, &c, "scripts/fill.py", []byte("print('fill')\n"))
	mustAdd(t, &c, "empty.txt", nil)

	got := c.Entries()
	if len(got) != 3 || got[0].Path != "SKILL.md" || got[1].Path != "scripts/fill.py" || got[2].Path != "empty.txt" {
		t.Fatalf("entries = %v", entryPaths(got))
	}
	if string(got[1].Data) != "print('fill')\n" || len(got[2].Data) != 0 {
		t.Errorf("bytes changed: %q, %q", got[1].Data, got[2].Data)
	}
}

// A sender that never stops is read one byte past the file's limit and no
// further. This is the property every entry point relies on — a multipart
// part has no declared size at all, and a zip's is the sender's claim.
func TestASenderThatNeverStopsIsReadOnlyOnePastTheLimit(t *testing.T) {
	t.Parallel()

	var c Collector
	r := &endless{}
	addRefused(t, &c, "assets/forever.bin", r, `"assets/forever.bin" is over 5 MiB`)

	if r.taken > MaxFileBytes+64<<10 {
		t.Errorf("took %d bytes from a sender that never stops; the limit is %d", r.taken, MaxFileBytes)
	}
}

// Once the skill is full, what is left is the budget, and it is smaller than
// a file's limit: the next sender gives one byte and is refused for the skill.
func TestAFullCollectorTakesOneByteFromTheNextSender(t *testing.T) {
	t.Parallel()

	var c Collector
	for _, e := range filling(MaxSkillBytes) {
		mustAdd(t, &c, e.Path, e.Data)
	}

	r := &endless{}
	addRefused(t, &c, "one-more.bin", r, "a skill is at most 20 MiB")
	if r.taken > 64<<10 {
		t.Errorf("took %d bytes from the sender after the skill was full", r.taken)
	}
}

// The 201st file is refused before a byte of it is read.
func TestTheFileAfterTheLastIsNotRead(t *testing.T) {
	t.Parallel()

	var c Collector
	for i := range MaxFiles {
		mustAdd(t, &c, fmt.Sprintf("f%03d", i), nil)
	}

	r := &endless{}
	addRefused(t, &c, "f200", r, "at most 200 files")
	if r.taken != 0 {
		t.Errorf("read %d bytes of a file that could never be kept", r.taken)
	}
}

// A connection that drops mid-file is an error naming the file, and nothing
// half-read is kept.
func TestAFileCutOffMidReadIsNotKept(t *testing.T) {
	t.Parallel()

	var c Collector
	addRefused(t, &c, "scripts/fill.py", &failing{}, `"scripts/fill.py" cannot be read`)
	if len(c.Entries()) != 0 {
		t.Errorf("kept %v from a read that failed", entryPaths(c.Entries()))
	}
}

// A refused file does not count against the skill: the total is only what
// was kept, so the caller's error is about this file and nothing else.
func TestARefusedFileAddsNothingToTheTotal(t *testing.T) {
	t.Parallel()

	var c Collector
	addRefused(t, &c, "big.bin", bytes.NewReader(make([]byte, MaxFileBytes+1)), "over 5 MiB")
	if c.total != 0 {
		t.Errorf("total = %d after a refused file, want 0", c.total)
	}
}

// Exactly at each limit is allowed; the refusals above are one past them.
func TestTheCollectorsLimitsThemselvesAreAllowed(t *testing.T) {
	t.Parallel()

	var file Collector
	mustAdd(t, &file, "big.bin", make([]byte, MaxFileBytes))

	var skill Collector
	for _, e := range filling(MaxSkillBytes) {
		mustAdd(t, &skill, e.Path, e.Data)
	}
	mustAdd(t, &skill, "empty-after-full.txt", nil)

	var count Collector
	for i := range MaxFiles {
		mustAdd(t, &count, fmt.Sprintf("f%03d", i), nil)
	}
}
