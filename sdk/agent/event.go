package agent

import (
	"encoding/json"
	"time"
)

type Event interface{ event() }

type TextEvent struct{ Delta string }

type ToolCallEvent struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolResultEvent struct {
	ID       string
	Name     string
	Output   string
	Err      string
	Duration time.Duration
}

type UsageEvent struct {
	Model string
	Usage Usage
}

type StepEvent struct{ Step int }

func (TextEvent) event()       {}
func (ToolCallEvent) event()   {}
func (ToolResultEvent) event() {}
func (UsageEvent) event()      {}
func (StepEvent) event()       {}

type Result struct {
	Output   string
	Messages []Message
	Usage    Usage
	Steps    int

	// Steers that arrived with no request left to carry them — sent during the
	// model's last call, or after the pattern had already decided to stop.
	// Returned rather than dropped, because a steer that silently vanishes is
	// indistinguishable from one the model read and chose to ignore.
	UnappliedSteers []string
}
