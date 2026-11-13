// Package agentstream turns what an sdk/agent run reports into stream events,
// so a program built on the SDK serves its runs to a UI in a few lines. It is
// a package of its own so that stream itself never links the agent SDK.
package agentstream

import (
	"github.com/LaplacianAI/openarity/sdk/agent"
	"github.com/LaplacianAI/openarity/sdk/stream"
)

// Convert is the stream's event for one of the SDK's, and false for one the
// stream has no event for. A steer is one: the run reports only its text, and
// which message it was is the caller's to say, with a MessageConsumed.
func Convert(e agent.Event) (stream.Event, bool) {
	switch e := e.(type) {
	case agent.TextEvent:
		return stream.TextMessageChunk{Delta: e.Delta}, true
	case agent.ReasoningEvent:
		return stream.ReasoningChunk{Delta: e.Delta}, true
	case agent.ToolCallEvent:
		return stream.ToolCallChunk{ToolCallID: e.ID, Name: e.Name, Arguments: string(e.Arguments)}, true
	case agent.ToolResultEvent:
		return stream.ToolCallResult{
			ToolCallID: e.ID, Name: e.Name, Content: e.Output, Error: e.Err,
			DurationMS: e.Duration.Milliseconds(),
		}, true
	case agent.StepEvent:
		return stream.StepStarted{Step: e.Step}, true
	case agent.UsageEvent:
		return stream.Usage{
			Model: e.Model, InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens,
			CachedInputTokens: e.Usage.CachedInputTokens,
		}, true
	case agent.CompactedEvent:
		return stream.Compacted{
			Reason: e.Reason, Cleared: e.Cleared, Folded: e.Folded, Before: e.Before, After: e.After,
			Summary: e.Summary, Error: e.Err,
		}, true
	}
	return nil, false
}

// Appender is where Pipe writes: a stream.Memory, or a log of your own.
type Appender interface {
	Append(runID string, e stream.Event) stream.Envelope
}

// Pipe appends each event the run reports that the stream has an event for,
// under runID, until events is closed. Hand Runner.Run the same channel and
// close it when Run returns.
//
// Text and reasoning are stamped with the message they belong to, the step
// they follow, which a single event cannot know.
func Pipe(events <-chan agent.Event, runID string, to Appender) {
	step := 0
	for e := range events {
		if s, ok := e.(agent.StepEvent); ok {
			step = s.Step
		}
		if s, ok := Convert(e); ok {
			to.Append(runID, stamp(s, runID, step))
		}
	}
}

func stamp(e stream.Event, runID string, step int) stream.Event {
	switch c := e.(type) {
	case stream.TextMessageChunk:
		c.MessageID = stream.MessageID(runID, step, stream.PartOfText)
		return c
	case stream.ReasoningChunk:
		c.MessageID = stream.MessageID(runID, step, stream.PartOfReasoning)
		return c
	}
	return e
}
