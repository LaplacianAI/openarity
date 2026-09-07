package stack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTextReporterNamesEachStepAsItStarts(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	report := TextReporter(&out)

	for _, step := range StepNames {
		report(Event{Step: step, Phase: PhaseStarted})
	}

	for _, step := range StepNames {
		if label := stepLabels[step]; !strings.Contains(out.String(), label) {
			t.Errorf("%q is missing from the output, so %s happens in silence", label, step)
		}
	}
}

// The reason this exists. A download reporting nothing for ninety seconds
// reads as a hang, and did — a CI job was cancelled twice before anyone could
// say which step was slow.
func TestTextReporterReportsProgressEveryTenPercent(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	report := TextReporter(&out)

	for percent := 1; percent <= 100; percent++ {
		report(Event{Step: StepDownload, Phase: PhaseProgress, Percent: percent})
	}

	lines := strings.Count(out.String(), "%")
	if lines != 10 {
		t.Errorf("reported %d percentages, want 10 — one per ten percent", lines)
	}
	for _, want := range []string{"10%", "50%", "100%"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%q is missing", want)
		}
	}
}

// Backwards is not progress. Two artefacts downloading through one reporter
// would otherwise print 90%, 10%, 20% and read as a restart.
func TestTextReporterNeverGoesBackwards(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	report := TextReporter(&out)

	report(Event{Step: StepDownload, Phase: PhaseProgress, Percent: 90})
	report(Event{Step: StepDownload, Phase: PhaseProgress, Percent: 10})

	if strings.Contains(out.String(), "10%") {
		t.Errorf("output = %q, want the later, smaller percentage dropped", out.String())
	}
}

func TestTextReporterSaysWhichStepFailed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	TextReporter(&out)(Event{
		Step: StepCluster, Phase: PhaseFailed, Detail: "initdb.exe: exit status 0xc0000135",
	})

	if !strings.Contains(out.String(), stepLabels[StepCluster]) {
		t.Errorf("output = %q, want it to name the step", out.String())
	}
	if !strings.Contains(out.String(), "0xc0000135") {
		t.Errorf("output = %q, want it to carry the error", out.String())
	}
}

// The passphrase reaches a person once, from the command itself. It must not
// also arrive in whatever the progress output is piped into — a CI log, a file,
// a window's transcript — so no step and no phase may carry it out of here.
//
// Driven over every combination rather than the one the passphrase happens to
// arrive on today. Written as that single case, this passed for a reason that
// had nothing to do with the passphrase: the ready step has no label, so the
// reporter returned before looking at the event at all.
func TestTextReporterNeverPrintsThePassphrase(t *testing.T) {
	t.Parallel()

	const passphrase = "hK4mNpQ7rTvXwY2z"

	var out bytes.Buffer
	report := TextReporter(&out)

	for _, step := range append(append([]string{}, StepNames...), StepReady) {
		for _, phase := range []Phase{PhaseStarted, PhaseProgress, PhaseDone, PhaseFailed} {
			// With and without a detail, because those are separate branches
			// and only one of them was reached when this was written one way.
			// Detail is never given the passphrase: it carries the artefact
			// being fetched and the text of a failure, and printing it is the
			// point. The field under test is Passphrase.
			for _, detail := range []string{"", "postgres"} {
				report(Event{
					Step: step, Phase: phase, Percent: 50,
					Detail:     detail,
					URL:        "http://127.0.0.1:21120/ui",
					Passphrase: passphrase,
				})
			}
		}
	}

	if strings.Contains(out.String(), passphrase) {
		t.Errorf("output = %q, want the passphrase absent from every step and phase", out.String())
	}
}

// A multi-line error must not be mistaken for several events by whatever is
// reading the output.
func TestTextReporterKeepsAFailureOnOneLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	TextReporter(&out)(Event{
		Step: StepMigrate, Phase: PhaseFailed, Detail: "first line\nsecond line",
	})

	if got := strings.Count(strings.TrimRight(out.String(), "\n"), "\n"); got != 0 {
		t.Errorf("output = %q, want one line", out.String())
	}
}

// The JSON reporter is what the installer window reads, and it must keep
// carrying the fields the text one drops.
func TestJSONReporterCarriesTheReadyEventWhole(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	JSONReporter(&out)(Event{
		Step: StepReady, Phase: PhaseDone,
		URL: "http://127.0.0.1:21120/ui", Passphrase: "hK4mNpQ7rTvXwY2z",
	})

	var got Event
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the reporter wrote something that is not JSON: %v", err)
	}
	if got.URL == "" || got.Passphrase == "" {
		t.Errorf("decoded %+v, want the URL and passphrase the window needs", got)
	}
}
