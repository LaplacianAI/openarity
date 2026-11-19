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
	// ParentID is the call this one was made inside, such as a script's;
	// empty for a call the model made itself.
	ParentID string
}

type ToolResultEvent struct {
	ID       string
	Name     string
	Output   string
	Err      string
	Duration time.Duration
	ParentID string
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

// SkippedEvent is a pattern's own step left out, and why, with the run going
// on from what it had: a review that came back unreadable keeps the answer it
// was reviewing, a plan that could not be read is answered without one.
type SkippedEvent struct {
	Step   int
	What   string // "critique", "rewrite" or "plan"
	Reason string
}

func (TextEvent) event()       {}
func (ReasoningEvent) event()  {}
func (ToolCallEvent) event()   {}
func (ToolResultEvent) event() {}
func (UsageEvent) event()      {}
func (StepEvent) event()       {}
func (SkippedEvent) event()    {}

type Result struct {
	Output          string
	Structured      json.RawMessage
	Plain           string
	Messages        []Message
	Usage           Usage
	Steps           int
	UnappliedSteers []string
}
