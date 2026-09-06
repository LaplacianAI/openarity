package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnEventIsOneLineOfJSON(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	report := JSONReporter(&b)

	report(Event{Step: "download", Phase: PhaseStarted, Detail: "PostgreSQL 18.6.0"})
	report(Event{Step: "download", Phase: PhaseProgress, Percent: 42})
	report(Event{Step: "download", Phase: PhaseDone})

	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), b.String())
	}

	for _, line := range lines {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line is not JSON: %v\n%s", err, line)
		}
	}
}

// A reader parses these one line at a time, so an event carrying a newline
// would be read as two — one of them malformed.
func TestAnEventCannotSpanTwoLines(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	JSONReporter(&b)(Event{Step: "migrate", Phase: PhaseFailed, Detail: "line one\nline two"})

	if got := strings.Count(strings.TrimSpace(b.String()), "\n"); got != 0 {
		t.Errorf("a multi-line detail produced %d newlines inside one event", got)
	}
}

// Nothing may report a step it did not name, because the shell around this
// draws a row per step and an empty one is a blank row.
func TestEveryStepIsNamed(t *testing.T) {
	t.Parallel()

	for _, step := range StepNames {
		if step == "" {
			t.Error("a step has no name")
		}
	}
	if len(StepNames) == 0 {
		t.Error("no steps are declared, so nothing can draw a progress list")
	}
}

// A reporter that was never set must not be a nil dereference in the middle
// of an install.
func TestNoReporterIsSafe(t *testing.T) {
	t.Parallel()

	var s Setup
	s.report(Event{Step: "download", Phase: PhaseStarted})
}
