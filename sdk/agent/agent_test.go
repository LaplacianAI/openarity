package agent

import (
	"context"
	"testing"
	"time"
)

func TestTextJoinsEveryTextBlock(t *testing.T) {
	t.Parallel()

	m := Message{Role: RoleAssistant, Content: []Content{
		{Type: ContentText, Text: "the answer is "},
		{Type: ContentText, Text: "42"},
	}}
	if got := m.Text(); got != "the answer is 42" {
		t.Errorf("Text() = %q", got)
	}
}

// Rendering a placeholder for a blob would put words in an audit record that
// nobody typed. A message that was only an image has no text, and Text says so.
func TestTextIgnoresBlobs(t *testing.T) {
	t.Parallel()

	m := Message{Role: RoleUser, Content: []Content{
		{Type: ContentImage, Blob: &Blob{MediaType: "image/png", Data: []byte{0x89, 'P'}}},
		{Type: ContentText, Text: "what is this?"},
		{Type: ContentFile, Blob: &Blob{MediaType: "application/pdf", Name: "q3.pdf"}},
	}}
	if got := m.Text(); got != "what is this?" {
		t.Errorf("Text() = %q, want only the words", got)
	}

	only := Message{Role: RoleUser, Content: []Content{
		{Type: ContentImage, Blob: &Blob{MediaType: "image/png"}},
	}}
	if got := only.Text(); got != "" {
		t.Errorf("an image-only message rendered as %q, want empty", got)
	}
}

func TestTextOnAMessageWithNoContent(t *testing.T) {
	t.Parallel()

	// An assistant turn that only called a tool carries no content at all.
	m := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup"}}}
	if got := m.Text(); got != "" {
		t.Errorf("Text() = %q, want empty", got)
	}
}

// A nil Events channel is the normal case for a batch run, and must not be a
// special case at any call site inside a loop.
func TestEmitOnANilChannelIsANoOp(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		Input{}.Emit(t.Context(), StepEvent{Step: 1})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on a nil channel")
	}
}

func TestEmitDelivers(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 1)
	in := Input{Events: events}
	in.Emit(t.Context(), StepEvent{Step: 7})

	got, ok := (<-events).(StepEvent)
	if !ok {
		t.Fatalf("got %T, want a StepEvent", got)
	}
	if got.Step != 7 {
		t.Errorf("Step = %d, want 7", got.Step)
	}
}

// A consumer that stopped reading must not be able to wedge a loop holding an
// unfinished tool call. Without the ctx.Done arm this blocks forever.
func TestEmitGivesUpWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Unbuffered and nobody reading: delivery is impossible.
	in := Input{Events: make(chan Event)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		in.Emit(ctx, StepEvent{Step: 1})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on a channel nobody was reading from")
	}
}

// The sealed interface is what stops an event describing a run this loop never
// performed. Nothing outside the package can satisfy it, and everything inside
// that claims to be an Event must actually be one.
func TestEveryEventTypeSatisfiesTheInterface(t *testing.T) {
	t.Parallel()

	for _, e := range []Event{
		TextEvent{},
		ToolCallEvent{},
		ToolResultEvent{},
		UsageEvent{},
		StepEvent{},
	} {
		if e == nil {
			t.Error("a nil Event reached the list")
		}
	}
}
