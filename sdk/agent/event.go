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
}
