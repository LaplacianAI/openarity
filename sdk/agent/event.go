package agent

import (
	"encoding/json"
	"time"
)

type Event interface{ event() }

type TextEvent struct{ Delta string }

// ReasoningEvent is thinking rather than talking: the model's own reasoning,
// when its provider sends it, and the text of the calls a pattern makes for
// itself, such as a plan or a critique.
type ReasoningEvent struct{ Delta string }

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
	// Summary marks the call a compaction made to write its summary: spent
	// like any other, but it read the whole context, so it says nothing about
	// how large the run's next call will be.
	Summary bool
}

type StepEvent struct{ Step int }

func (TextEvent) event()       {}
func (ReasoningEvent) event()  {}
func (ToolCallEvent) event()   {}
func (ToolResultEvent) event() {}
func (UsageEvent) event()      {}
func (StepEvent) event()       {}

type Result struct {
	Output          string
	Structured      json.RawMessage
	Plain           string
	Messages        []Message
	Usage           Usage
	Steps           int
	UnappliedSteers []string
}
