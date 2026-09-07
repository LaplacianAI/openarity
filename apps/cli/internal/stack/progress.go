package stack

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

type Phase string

const (
	PhaseStarted  Phase = "started"
	PhaseProgress Phase = "progress"
	PhaseDone     Phase = "done"
	PhaseFailed   Phase = "failed"
)

type Event struct {
	Step    string `json:"step"`
	Phase   Phase  `json:"state"`
	Detail  string `json:"detail,omitempty"`
	Percent int    `json:"percent,omitempty"`

	URL string `json:"url,omitempty"`

	Passphrase string `json:"passphrase,omitempty"`
}

type Reporter func(Event)

var StepNames = []string{
	StepResolve,
	StepDownload,
	StepCluster,
	StepMigrate,
	StepIdentity,
	StepStart,
}

const (
	StepResolve  = "resolve"
	StepDownload = "download"
	StepCluster  = "cluster"
	StepMigrate  = "migrate"
	StepIdentity = "identity"
	StepStart    = "start"
	StepReady    = "ready"
)

func JSONReporter(w io.Writer) Reporter {
	var mu sync.Mutex
	encoder := json.NewEncoder(w)

	return func(e Event) {
		e.Detail = strings.ReplaceAll(e.Detail, "\n", " ")

		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(e)
	}
}

func (s *Setup) report(e Event) {
	if s.Report == nil {
		return
	}
	s.Report(e)
}

// What each step is called when a person is reading rather than a program.
var stepLabels = map[string]string{
	StepResolve:  "Finding what it needs",
	StepDownload: "Downloading",
	StepCluster:  "Creating the database",
	StepMigrate:  "Setting up its tables",
	StepIdentity: "Creating your sign-in",
	StepStart:    "Starting Openarity",
}

// TextReporter writes the same events as prose.
//
// Without one, setup prints nothing between the wizard and the passphrase.
// That covers a 70MB download, a cluster being initialised and sixteen
// migrations — long enough on a slow machine that the only honest reading of
// the silence is that it has hung. It was read that way by a CI log before it
// was read that way by a person.
//
// Percentages are reported every ten, not every one: a hundred lines for one
// file is a progress bar that costs more than the download.
func TextReporter(w io.Writer) Reporter {
	var (
		mu       sync.Mutex
		reported = map[string]int{}
	)

	return func(e Event) {
		label := stepLabels[e.Step]
		if label == "" {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		switch e.Phase {
		case PhaseStarted:
			if e.Detail != "" {
				_, _ = fmt.Fprintf(w, "%s %s\n", label, e.Detail)
				return
			}
			_, _ = fmt.Fprintln(w, label)

		case PhaseProgress:
			if e.Percent <= 0 {
				return
			}
			if step := e.Percent / 10; step > reported[e.Step] {
				reported[e.Step] = step
				_, _ = fmt.Fprintf(w, "  %d%%\n", step*10)
			}

		case PhaseFailed:
			_, _ = fmt.Fprintf(w, "%s failed: %s\n", label, strings.ReplaceAll(e.Detail, "\n", " "))

		case PhaseDone:
		}
	}
}
