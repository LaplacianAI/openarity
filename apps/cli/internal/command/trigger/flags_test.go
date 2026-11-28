package trigger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestALengthPastWhatADurationHoldsIsRefused(t *testing.T) {
	for _, v := range []string{"106751d", "15250w"} { // the most whole days and weeks a duration holds
		if _, err := Length("every", v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	for _, v := range []string{"106752d", "15251w", "-1d", "99999999999999999999d"} {
		if _, err := Length("every", v); err == nil || !strings.Contains(err.Error(), "a length such as") {
			t.Errorf("%s: %v", v, err)
		}
	}
}

// A wall time the clocks skip is refused; one they pass twice is taken, as
// one of the two.
func TestAWallTimeIsOneTheZoneHas(t *testing.T) {
	zone := func() (string, error) { return "Europe/London", nil }
	if _, err := atOf("2026-03-29 01:30", zone); err == nil || !strings.Contains(err.Error(), "the clocks skip it") {
		t.Errorf("a skipped time: %v", err)
	}
	at, err := atOf("2026-10-25 01:30", zone)
	if err != nil {
		t.Fatal(err)
	}
	if first, second := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC); !at.Equal(first) && !at.Equal(second) {
		t.Errorf("a time passed twice = %v, want %v or %v", at.UTC(), first, second)
	}
	// One digit for the hour is the same time, not a skipped one.
	if _, err := atOf("2026-12-01 9:00", zone); err != nil {
		t.Errorf("one digit for the hour: %v", err)
	}
}

// A file is read no further than the cap, so a huge one, or /dev/zero, does
// not fill memory before it is refused.
func TestAFileIsReadNoFurtherThanTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.md")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != readCap {
		t.Errorf("read %d bytes, want %d", len(b), readCap)
	}
}
