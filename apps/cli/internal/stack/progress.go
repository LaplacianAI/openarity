package stack

import (
	"encoding/json"
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
